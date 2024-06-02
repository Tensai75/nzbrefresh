package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tensai75/cmpb"
	"github.com/Tensai75/nntp"
	"github.com/Tensai75/nntpPool"
	"github.com/Tensai75/nzbparser"
	"github.com/fatih/color"
)

type (
	Provider struct {
		Name                  string
		Host                  string
		Port                  uint32
		SSL                   bool
		SkipSslCheck          bool
		Username              string
		Password              string
		MaxConns              uint32
		ConnWaitTime          time.Duration
		IdleTimeout           time.Duration
		HealthCheck           bool
		MaxTooManyConnsErrors uint32
		MaxConnErrors         uint32

		pool         nntpPool.ConnectionPool
		capabilities struct {
			ihave bool
			post  bool
		}
		articles struct {
			checked   atomic.Uint64
			available atomic.Uint64
			missing   atomic.Uint64
			refreshed atomic.Uint64
		}
	}

	Config struct {
		providers []Provider
	}
)

type (
	segmentChanItem struct {
		segment  nzbparser.NzbSegment
		fileName string
	}
	providerStatistic map[string]uint64
	fileStatistic     struct {
		available     providerStatistic
		totalSegments uint64
	}
	filesStatistic map[string]*fileStatistic
)

var (
	appName      = "NZBRefresh"
	appVersion   = ""           // Github tag
	nzbfile      *nzbparser.Nzb // the parsed NZB file structure
	providerList []Provider     // the parsed provider list structure

	ihaveProviders []*Provider // Providers with IHAVE capability
	postProviders  []*Provider // Providers with POST capability

	err           error
	maxConns      uint32
	maxConnsLock  sync.Mutex
	segmentChan   chan segmentChanItem
	segmentChanWG sync.WaitGroup
	sendArticleWG sync.WaitGroup

	preparationStartTime  time.Time
	segmentCheckStartTime time.Time
	segmentBar            *cmpb.Bar
	uploadBar             *cmpb.Bar
	uploadBarStarted      bool
	uploadBarMutex        sync.Mutex
	progressBars          = cmpb.NewWithParam(&cmpb.Param{
		Interval:     200 * time.Microsecond,
		Out:          color.Output,
		ScrollUp:     cmpb.AnsiScrollUp,
		PrePad:       0,
		KeyWidth:     18,
		MsgWidth:     5,
		PreBarWidth:  15,
		BarWidth:     42,
		PostBarWidth: 25,
		Post:         "...",
		KeyDiv:       ':',
		LBracket:     '[',
		RBracket:     ']',
		Empty:        '-',
		Full:         '=',
		Curr:         '>',
	})
	fileStat     = make(filesStatistic)
	fileStatLock sync.Mutex
)

func init() {
	parseArguments()
	fmt.Println(args.Version())

	if args.Debug {
		logFileName := strings.TrimSuffix(filepath.Base(args.NZBFile), filepath.Ext(filepath.Base(args.NZBFile))) + ".log"
		f, err := os.Create(logFileName)
		if err != nil {
			exit(fmt.Errorf("unable to open debug log file: %v", err))
		}
		log.SetOutput(f)
	} else {
		log.SetOutput(io.Discard)
	}

	log.Print("preparing...")
	preparationStartTime = time.Now()
	// parse the argument

	// load the NZB file
	if nzbfile, err = loadNzbFile(args.NZBFile); err != nil {
		exit(fmt.Errorf("unable to load NZB file '%s': %v'", args.NZBFile, err))
	}

	// load the provider list
	if providerList, err = loadProviderList(args.Provider); err != nil {
		exit(fmt.Errorf("unable to load provider list: %v", err))
	}

	go func() {
		for {
			select {
			case v := <-nntpPool.LogChan:
				log.Printf("NNTPPool: %v", v)
			// case d := <-nntpPool.DebugChan:
			// log.Printf("NNTPPool: %v", d)
			case w := <-nntpPool.WarnChan:
				log.Printf("NNTPPool Error: %v", w)
			}
		}
	}()

	// setup the nntp connection pool for each provider
	var providerWG sync.WaitGroup
	for n := range providerList {
		n := n
		providerWG.Add(1)
		go func(provider *Provider) {
			defer providerWG.Done()
			if pool, err := nntpPool.New(&nntpPool.Config{
				Name:                  provider.Name,
				Host:                  provider.Host,
				Port:                  provider.Port,
				SSL:                   provider.SSL,
				SkipSSLCheck:          provider.SkipSslCheck,
				User:                  provider.Username,
				Pass:                  provider.Password,
				MaxConns:              provider.MaxConns,
				ConnWaitTime:          time.Duration(provider.ConnWaitTime) * time.Second,
				IdleTimeout:           time.Duration(provider.IdleTimeout) * time.Second,
				HealthCheck:           provider.HealthCheck,
				MaxTooManyConnsErrors: provider.MaxTooManyConnsErrors,
				MaxConnErrors:         provider.MaxConnErrors,
			}, 0); err != nil {
				exit(fmt.Errorf("unable to create the connection pool for provider '%s': %v", providerList[n].Name, err))
			} else {
				provider.pool = pool
			}

			// calculate the max connections
			maxConnsLock.Lock()
			if maxConns < provider.MaxConns {
				maxConns = provider.MaxConns
			}
			maxConnsLock.Unlock()

			// check the ihave and post capabilities of the provider
			if ihave, post, err := checkCapabilities(&providerList[n]); err != nil {
				exit(fmt.Errorf("unable to check capabilities of provider '%s': %v", providerList[n].Name, err))
			} else {
				providerList[n].capabilities.ihave = ihave
				providerList[n].capabilities.post = post
				log.Printf("capabilities of '%s': IHAVE: %v | POST: %v", providerList[n].Name, ihave, post)
			}
		}(&providerList[n])
	}
	providerWG.Wait()

	// check if we have at least one provider with IHAVE or POST capability
	for n := range providerList {
		if providerList[n].capabilities.ihave {
			ihaveProviders = append(ihaveProviders, &providerList[n])
		}
		if providerList[n].capabilities.post {
			postProviders = append(postProviders, &providerList[n])
		}
	}
	if len(ihaveProviders) == 0 && len(postProviders) == 0 {
		log.Print("no provider has IHAVE or POST capability")
	}

	// make the channels
	segmentChan = make(chan segmentChanItem, 8*maxConns)

	// run the go routines
	for i := uint32(0); i < 4*maxConns; i++ {
		go processSegment()
	}

	log.Printf("preparation took %v", time.Since(preparationStartTime))
}

func main() {

	startString := fmt.Sprintf("starting segment check of %v segments", nzbfile.TotalSegments)
	if args.CheckOnly {
		startString = startString + " (check only, no re-upload)"
	}
	fmt.Println(strings.ToUpper(startString[:1]) + startString[1:])
	log.Print(startString)
	segmentCheckStartTime = time.Now()

	// segment check progressbar
	segmentBar = progressBars.NewBar("Checking segments", nzbfile.TotalSegments)
	segmentBar.SetPreBar(cmpb.CalcSteps)
	segmentBar.SetPostBar(cmpb.CalcTime)

	// start progressbar
	progressBars.Start()

	// loop through all file tags within the NZB file
	for _, file := range nzbfile.Files {
		fileStatLock.Lock()
		fileStat[file.Filename] = new(fileStatistic)
		fileStat[file.Filename].available = make(providerStatistic)
		fileStat[file.Filename].totalSegments = uint64(file.TotalSegments)
		fileStatLock.Unlock()
		// loop through all segment tags within each file tag
		for _, segment := range file.Segments {
			segmentChanWG.Add(1)
			segmentChan <- segmentChanItem{segment, file.Filename}
		}
	}
	segmentChanWG.Wait()
	segmentBar.SetMessage("done")
	sendArticleWG.Wait()
	if !args.CheckOnly {
		uploadBar.SetMessage("done")
	}
	progressBars.Wait()
	log.Printf("segment check took %v | %v ms/segment", time.Since(segmentCheckStartTime), float32(time.Since(segmentCheckStartTime).Milliseconds())/float32(nzbfile.Segments))
	for n := range providerList {
		result := fmt.Sprintf("Results for '%s': checked: %v | available: %v | missing: %v | refreshed: %v | %v connections used",
			providerList[n].Name,
			providerList[n].articles.checked.Load(),
			providerList[n].articles.available.Load(),
			providerList[n].articles.missing.Load(),
			providerList[n].articles.refreshed.Load(),
			providerList[n].pool.MaxConns(),
		)
		fmt.Println(result)
		log.Print(result)
	}
	for n := range providerList {
		go providerList[n].pool.Close()
	}
	runtime := fmt.Sprintf("Total runtime %v | %v ms/segment", time.Since(preparationStartTime), float32(time.Since(preparationStartTime).Milliseconds())/float32(nzbfile.Segments))
	fmt.Println(runtime)
	log.Print(runtime)
	writeCsvFile()
}

func loadNzbFile(path string) (*nzbparser.Nzb, error) {
	if b, err := os.Open(path); err != nil {
		return nil, err
	} else {
		defer b.Close()
		if nzbfile, err := nzbparser.Parse(b); err != nil {
			return nil, err
		} else {
			return nzbfile, nil
		}
	}
}

func loadProviderList(path string) ([]Provider, error) {
	if file, err := os.ReadFile(path); err != nil {
		return nil, err
	} else {
		cfg := Config{}
		if err := json.Unmarshal(file, &cfg.providers); err != nil {
			return nil, err
		}
		return cfg.providers, nil
	}
}

func checkCapabilities(provider *Provider) (bool, bool, error) {
	if conn, err := provider.pool.Get(context.TODO()); err != nil {
		return false, false, err
	} else {
		defer provider.pool.Put(conn)
		var ihave, post bool
		if capabilities, err := conn.Capabilities(); err == nil {
			for _, capability := range capabilities {
				if strings.ToLower(capability) == "ihave" {
					ihave = true
				}
				if strings.ToLower(capability) == "post" {
					post = true
				}
			}
		} else {
			// nntp server is not RFC 3977 compliant
			// check post capability
			article := new(nntp.Article)
			if err := conn.Post(article); err != nil {
				if err.Error()[0:3] != "440" {
					post = true
				}
			} else {
				post = true
			}
			// check ihave capability
			if err := conn.IHave(article); err != nil {
				if err.Error()[0:3] != "500" {
					ihave = true
				}
			} else {
				ihave = true
			}
		}
		return ihave, post, nil
	}
}

func processSegment() {
	for segmentChanItem := range segmentChan {
		segment := segmentChanItem.segment
		fileName := segmentChanItem.fileName
		func() {
			defer func() {
				segmentChanWG.Done()
				segmentBar.Increment()
			}()
			// positiv provider list (providers who have the article)
			var availableOn []*Provider
			var availableOnLock sync.Mutex
			// negative provider list (providers who don't have the article)
			var missingOn []*Provider
			var missingOnLock sync.Mutex
			// segment check waitgroup
			var segmentCheckWG sync.WaitGroup
			// loop through each provider in the provider list
			for n := range providerList {
				n := n
				segmentCheckWG.Add(1)
				go func() {
					defer segmentCheckWG.Done()
					// check if message is available on the provider
					if isAvailable, err := checkMessageID(&providerList[n], segment.Id); err != nil {
						// error handling
						log.Print(fmt.Errorf("unable to check article <%s> on provider '%s': %v", segment.Id, providerList[n].Name, err))
						// TODO: What do we do with such errors??
					} else {
						providerList[n].articles.checked.Add(1)
						if isAvailable {
							providerList[n].articles.available.Add(1)
							fileStatLock.Lock()
							fileStat[fileName].available[providerList[n].Name]++
							fileStatLock.Unlock()
							// if yes add the provider to the positiv list
							availableOnLock.Lock()
							availableOn = append(availableOn, &providerList[n])
							availableOnLock.Unlock()
						} else {
							providerList[n].articles.missing.Add(1)
							// if yes add the provider to the positiv list
							missingOnLock.Lock()
							missingOn = append(missingOn, &providerList[n])
							missingOnLock.Unlock()
						}
					}
				}()
			}
			segmentCheckWG.Wait()
			// if negativ list contains entries at least one provider is missing the article
			if !args.CheckOnly && len(missingOn) > 0 {
				log.Printf("article <%s> is missing on at least one provider", segment.Id)
				// check if positiv list contains entries
				// without at least on provider having the article we cannot fix the others
				if len(availableOn) > 0 {
					uploadBarMutex.Lock()
					if uploadBarStarted {
						uploadBar.IncrementTotal()
					} else {
						uploadBar = progressBars.NewBar("Uploading articles", 1)
						uploadBar.SetPreBar(cmpb.CalcSteps)
						uploadBar.SetPostBar(cmpb.CalcTime)
						uploadBarStarted = true
					}
					uploadBarMutex.Unlock()
					// load article
					if article, err := loadArticle(availableOn, segment.Id); err != nil {
						log.Print(err)
						uploadBar.Increment()
					} else {
						// reupload article
						sendArticleWG.Add(1)
						go func() {
							// reupload article
							if err := reuploadArticle(missingOn, article, segment.Id); err != nil {
								// on error, try re-uploading on one of the providers having the article
								if err := reuploadArticle(availableOn, article, segment.Id); err != nil {
									log.Print(err)
								}
							}
							uploadBar.Increment()
							sendArticleWG.Done()
						}()
					}
				} else {
					// error handling if article is missing on all providers
					log.Print(fmt.Errorf("article <%s> is missing on all providers", segment.Id))
				}
			}
		}()
	}
}

func checkMessageID(provider *Provider, messageID string) (bool, error) {
	if conn, err := provider.pool.Get(context.TODO()); err != nil {
		return false, err
	} else {
		defer provider.pool.Put(conn)
		if article, err := conn.Head("<" + messageID + ">"); article != nil {
			// if header is availabel return true
			return true, nil
		} else {
			if err.Error()[0:3] == "430" {
				// upon error "430 No Such Article" return false
				return false, nil
			} else {
				// upon any other error return error
				return false, err
			}
		}
	}
}

func loadArticle(providerList []*Provider, messageID string) (*nntp.Article, error) {
	for _, provider := range providerList {
		// try to load the article from the provider
		log.Printf("loading article <%s> from provider '%s'", messageID, provider.Name)
		if article, err := getArticleFromProvider(provider, messageID); err != nil {
			// if the article cannot be loaded continue with the next provider on the list
			log.Print(fmt.Errorf("unable to load article <%s> from provider '%s': %v", messageID, provider.Name, err))
			continue
		} else {
			return article, err
		}
	}
	return nil, fmt.Errorf("unable to load article <%s> from any provider", messageID)
}

func getArticleFromProvider(provider *Provider, messageID string) (*nntp.Article, error) {
	if conn, err := provider.pool.Get(context.TODO()); err != nil {
		return nil, err
	} else {
		defer provider.pool.Put(conn)
		if article, err := conn.Article("<" + messageID + ">"); err != nil {
			return nil, err
		} else {
			return copyArticle(article, []byte{})
		}
	}
}

func reuploadArticle(providerList []*Provider, article *nntp.Article, segmentID string) error {
	var body []byte
	body, err := io.ReadAll(article.Body)
	if err != nil {
		return err
	}
	article.Body = bytes.NewReader(body)
	for n, provider := range providerList {
		if provider.capabilities.post {
			if copiedArticle, err := copyArticle(article, body); err != nil {
				return err
			} else {
				// send the article to the provider
				log.Printf("re-uploading article <%s> to provider '%s' (%v. attempt)", segmentID, provider.Name, n+1)
				if err := postArticleToProvider(provider, copiedArticle); err != nil {
					// error handling if re-uploading the article was unsuccessfull
					log.Print(fmt.Errorf("error re-uploading article <%s> to provider '%s': %v", segmentID, provider.Name, err))
				} else {
					provider.articles.refreshed.Add(1)
					// handling of successfull send
					log.Printf("article <%s> successfully sent to provider '%s'", segmentID, provider.Name)
					// if post was successfull return
					// other providers missing this article will get it from this provider
					return nil
				}
			}
		}
	}
	return fmt.Errorf("unable to re-upload article <%s> to any provider", segmentID)
}

func postArticleToProvider(provider *Provider, article *nntp.Article) error {
	if conn, err := provider.pool.Get(context.TODO()); err != nil {
		return err
	} else {
		defer provider.pool.Put(conn)
		// for post, first clean the headers
		cleanHeaders(article)
		// post the article
		if err := conn.Post(article); err != nil {
			return err
		} else {
			return nil
		}
	}
}

func cleanHeaders(article *nntp.Article) {
	// minimum headers required for post
	headers := []string{
		"From",
		"Subject",
		"Newsgroups",
		"Message-Id",
		"Date",
		"Path",
	}
	for header := range article.Header {
		if slices.Contains(headers, header) {
			// clean Path header
			if header == "Path" {
				article.Header[header] = []string{"not-for-mail"}
			}
			// update Date header to now
			if header == "Date" {
				article.Header[header] = []string{time.Now().Format(time.RFC1123Z)}
			}
		} else {
			delete(article.Header, header)
		}
	}
}

func copyArticle(article *nntp.Article, body []byte) (*nntp.Article, error) {
	var err error
	if len(body) == 0 {
		body, err = io.ReadAll(article.Body)
		if err != nil {
			return nil, err
		}
	}
	newArticle := nntp.Article{
		Header: make(map[string][]string),
	}
	for header := range article.Header {
		newArticle.Header[header] = append(newArticle.Header[header], article.Header[header]...)
	}
	newArticle.Body = bytes.NewReader(body)
	return &newArticle, nil
}

func writeCsvFile() {
	if args.Csv {
		csvFileName := strings.TrimSuffix(filepath.Base(args.NZBFile), filepath.Ext(filepath.Base(args.NZBFile))) + ".csv"
		f, err := os.Create(csvFileName)
		if err != nil {
			exit(fmt.Errorf("unable to open csv file: %v", err))
		}
		log.Println("writing csv file...")
		fmt.Print("Writing csv file... ")
		csvWriter := csv.NewWriter(f)
		firstLine := true
		// make sorted provider name slice
		providers := make([]string, 0, len(providerList))
		for n := range providerList {
			providers = append(providers, providerList[n].Name)
		}
		sort.Strings(providers)
		for fileName, file := range fileStat {
			// write first line
			if firstLine {
				line := make([]string, len(providers)+2)
				line[0] = "Filename"
				line[1] = "Total segments"
				for n, providerName := range providers {
					line[n+2] = providerName
				}
				if err := csvWriter.Write(line); err != nil {
					exit(fmt.Errorf("unable to write to the csv file: %v", err))
				}
				firstLine = false
			}
			// write line
			line := make([]string, len(providers)+2)
			line[0] = fileName
			line[1] = fmt.Sprintf("%v", file.totalSegments)
			for n, providerName := range providers {
				if value, ok := file.available[providerName]; ok {
					line[n+2] = fmt.Sprintf("%v", value)
				} else {
					line[n+2] = "0"
				}
			}
			if err := csvWriter.Write(line); err != nil {
				exit(fmt.Errorf("unable to write to the csv file: %v", err))
			}
		}
		csvWriter.Flush()
		if err := csvWriter.Error(); err != nil {
			exit(fmt.Errorf("unable to write to the csv file: %v", err))
		}
		f.Close()
		fmt.Print("done")
	}
}

func exit(err error) {
	if err != nil {
		fmt.Printf("Fatal error: %v\n", err)
		log.Fatal(err)
	} else {
		os.Exit(0)
	}
}
