package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tensai75/nntp"
	"github.com/Tensai75/nntpPool"
	"github.com/Tensai75/nzbparser"
	progressbar "github.com/schollz/progressbar/v3"
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

var (
	nzbFilePath      string         // the path to the NZB file provided as argument to the program
	nzbfile          *nzbparser.Nzb // the parsed NZB file structure
	providerListPath string         // the path to the provider list config file (hardcoded './config.json' or provided as second argment)
	providerList     []Provider     // the parsed provider list structure

	err           error
	totalConn     uint64
	segmentChan   chan nzbparser.NzbSegment
	segmentChanWG sync.WaitGroup
	sendArticleWG sync.WaitGroup

	preparationStartTime  time.Time
	segmentCheckStartTime time.Time
	bar                   *progressbar.ProgressBar
)

func init() {
	log.Printf("preparing connections")
	preparationStartTime = time.Now()
	// parse the argument
	if len(os.Args) > 1 {
		nzbFilePath = os.Args[1]
	} else {
		log.Fatal(fmt.Errorf("no path to NZB file provided"))
	}
	if len(os.Args) > 2 {
		providerListPath = os.Args[2]
	}

	// load the NZB file
	if nzbfile, err = loadNzbFile(nzbFilePath); err != nil {
		log.Fatal(fmt.Errorf("unable to load NZB file '%s': %v'", nzbFilePath, err))
	}

	// load the provider list
	if providerListPath == "" {
		providerListPath = "config.json"
	}
	if providerList, err = loadProviderList(providerListPath); err != nil {
		log.Fatal(fmt.Errorf("unable to load provider list: %v", err))
	}

	go func() {
		for {
			select {
			case v := <-nntpPool.LogChan:
				log.Printf("NNTPPool: %v", v)
			//case d := <-nntpPool.DebugChan:
			//	log.Printf("NNTPPool: %v", d)
			case w := <-nntpPool.WarnChan:
				log.Printf("NNTPPool Error: %v", w)
			}
		}
	}()

	// setup the nntp connection pool for each provider
	var providerWG sync.WaitGroup
	for n := range providerList {
		providerWG.Add(1)
		go func() {
			defer providerWG.Done()
			if providerList[n].pool, err = nntpPool.New(&nntpPool.Config{
				Name:                  providerList[n].Name,
				Host:                  providerList[n].Host,
				Port:                  providerList[n].Port,
				SSL:                   providerList[n].SSL,
				SkipSSLCheck:          providerList[n].SkipSslCheck,
				User:                  providerList[n].Username,
				Pass:                  providerList[n].Password,
				MaxConns:              providerList[n].MaxConns,
				ConnWaitTime:          time.Duration(providerList[n].ConnWaitTime) * time.Second,
				IdleTimeout:           time.Duration(providerList[n].IdleTimeout) * time.Second,
				MaxTooManyConnsErrors: providerList[n].MaxTooManyConnsErrors,
				MaxConnErrors:         providerList[n].MaxConnErrors,
			}, 0); err != nil {
				log.Fatal(fmt.Errorf("unable to create the connection pool for provider '%s': %v", providerList[n].Name, err))
			}

			// calculate the total connections
			totalConn = totalConn + uint64(providerList[n].MaxConns)

			// check the ihave and post capabilities of the provider
			if ihave, post, err := checkCapabilities(&providerList[n]); err != nil {
				log.Fatal(fmt.Errorf("unable to check capabilities of provider '%s': %v", providerList[n].Name, err))
			} else {
				providerList[n].capabilities.ihave = ihave
				providerList[n].capabilities.post = post
				log.Printf("capabilities of '%s': IHAVE: %v | POST: %v", providerList[n].Name, ihave, post)
			}
		}()
	}
	providerWG.Wait()

	// make the channels
	segmentChan = make(chan nzbparser.NzbSegment, 4*totalConn)

	// run the go routines
	go processSegment()

	log.Printf("preparation took %v", time.Since(preparationStartTime))
}

func main() {
	log.Printf("starting segment check")
	segmentCheckStartTime = time.Now()
	bar = progressbar.NewOptions(nzbfile.TotalSegments,
		progressbar.OptionSetDescription("Checking segments"),
		progressbar.OptionSetRenderBlankState(true),
		//progressbar.OptionThrottle(time.Millisecond*100),
		progressbar.OptionShowElapsedTimeOnFinish(),
	)
	// loop through all file tags within the NZB file
	for _, files := range nzbfile.Files {
		// loop through all segment tags within each file tag
		for _, segment := range files.Segments {
			segmentChanWG.Add(1)
			segmentChan <- segment
		}
	}
	segmentChanWG.Wait()
	sendArticleWG.Wait()
	bar.Finish()
	fmt.Println()
	log.Printf("segment check took %v | %v ms/segment", time.Since(segmentCheckStartTime), float32(time.Since(segmentCheckStartTime).Milliseconds())/float32(nzbfile.Segments))
	for n := range providerList {
		maxUsedConns := providerList[n].pool.MaxConns()
		log.Printf("Results for '%s': segments: %v | checked: %v | available: %v | missing: %v | refreshed: %v | %v connections used", providerList[n].Name, nzbfile.Segments, providerList[n].articles.checked.Load(), providerList[n].articles.available.Load(), providerList[n].articles.missing.Load(), providerList[n].articles.refreshed.Load(), maxUsedConns)
	}
	for n := range providerList {
		go providerList[n].pool.Close()
	}
	log.Printf("total runtime %v | %v ms/segment", time.Since(preparationStartTime), float32(time.Since(preparationStartTime).Milliseconds())/float32(nzbfile.Segments))
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
		if capabilities, err := conn.Capabilities(); err != nil {
			return false, false, err
		} else {
			var ihave, post bool
			for _, capability := range capabilities {
				if strings.ToLower(capability) == "ihave" {
					ihave = true
				}
				if strings.ToLower(capability) == "post" {
					post = true
				}
			}
			return ihave, post, nil
		}
	}
}

func processSegment() {
	for segment := range segmentChan {
		go func() {
			defer func() {
				segmentChanWG.Done()
				bar.Add(1)
			}()
			// prepare positiv list (for providers who have the article)
			var availableOn []*Provider
			// prepare negativ list (for providers who don't have the article)
			var missingOn []*Provider
			// set segment check waitgroup
			var segmentCheckWG sync.WaitGroup
			// loop through each provider in the provider list
			for n := range providerList {
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
							// if yes add the provider to the positiv list
							availableOn = append(availableOn, &providerList[n])
						} else {
							providerList[n].articles.missing.Add(1)
							// if not add the provider to the negativ list
							missingOn = append(missingOn, &providerList[n])
						}
					}
				}()
			}
			segmentCheckWG.Wait()
			// if negativ list contains entries at least one provider is missing the article
			if len(missingOn) > 0 {
				log.Printf("article <%s> is missing on at least one provider", segment.Id)
				// check if positiv list contains entries
				// without at least on provider having the article we cannot fix the others
				if len(availableOn) > 0 {
					loaded := false
					// loop through the provider on the positiv list
					for _, provider := range availableOn {
						// try to load the article from the provider
						log.Printf("loading article <%s> from provider '%s'", segment.Id, provider.Name)
						article, err := getArticleFromProvider(provider, segment.Id)
						if err != nil {
							// if the article cannot be loaded continue with the next provider on the positiv list
							log.Print(fmt.Errorf("unable to load article <%s> from provider '%s': %v", segment.Id, provider.Name, err))
							continue
						}
						// if article was loaded loop through the provider on the negativ list
						for _, provider := range missingOn {
							sendArticleWG.Add(1)
							go func() {
								defer sendArticleWG.Done()
								// send the article to the provider
								log.Printf("sending article <%s> to provider '%s'", segment.Id, provider.Name)
								err := sendArticleToProvider(provider, article)
								if err != nil {
									// error handling if sending the article was unsuccessfull
									log.Print(fmt.Errorf("error sending article <%s> to provider '%s': %v", segment.Id, provider.Name, err))
								} else {
									provider.articles.refreshed.Add(1)
									// handling of successfull send
									log.Printf("article <%s> successfully sent to provider '%s'", segment.Id, provider.Name)
								}
							}()
						}
						// article was loaded so break out of the loop for article loading
						loaded = true
						break
					}
					// error handling if the article cannot be loaded from any provider
					if !loaded {
						log.Print(fmt.Errorf("unable to load article <%s> from any provider", segment.Id))
					}
				} else {
					// error handling if article is missing on all providers
					log.Print(fmt.Errorf("article <%s> is missing on all providers", segment.Id))
				}
			} else {
				// article is available on all providers
				// log.Printf("article <%s> is available on all providers", segment.Id)
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

func getArticleFromProvider(provider *Provider, messageID string) (*nntp.Article, error) {
	if conn, err := provider.pool.Get(context.TODO()); err != nil {
		return nil, err
	} else {
		defer provider.pool.Put(conn)
		if article, err := conn.Article("<" + messageID + ">"); err != nil {
			return nil, err
		} else {
			return article, nil
		}
	}
}

func sendArticleToProvider(provider *Provider, article *nntp.Article) error {

	if !provider.capabilities.ihave && !provider.capabilities.post {
		return fmt.Errorf("provider has neither IHAVE nor POST capability")
	}
	if conn, err := provider.pool.Get(context.TODO()); err != nil {
		return err
	} else {
		defer provider.pool.Put(conn)
		// update the date information in the article header
		if _, ok := article.Header["Date"]; !ok {
			article.Header["Date"] = make([]string, 1)
		}
		article.Header["Date"][0] = time.Now().Format(time.RFC1123Z)
		// upload article either with IHAVE or POST
		if provider.capabilities.ihave {
			// TODO: implemented IHAVE command into nntp module
			// if err := conn.IHave(article); err != nil {
			//	 return err
			// } else {
			//   return nil
			// }
		}
		if provider.capabilities.post {
			if err := conn.Post(article); err != nil {
				return err
			} else {
				return nil
			}
		}
		return fmt.Errorf("IHAVE function not yet implemented")
	}
}
