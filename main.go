package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Tensai75/nntp"
	"github.com/Tensai75/nntpPool"
	"github.com/Tensai75/nzbparser"
)

type (
	Provider struct {
		Name         string
		Host         string
		Port         uint32
		SSL          bool
		SkipSslCheck bool
		Username     string
		Password     string
		Retries      uint32
		WaitTime     uint32
		MinConns     uint32
		MaxConns     uint32
		IdleTimeout  uint32

		pool         nntpPool.ConnectionPool
		capabilities struct {
			ihave bool
			post  bool
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

	err error
)

func init() {
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

	// setup the nntp connection pool for each provider
	for n, provider := range providerList {
		if providerList[n].pool, err = nntpPool.New(&nntpPool.Config{
			Host:         provider.Host,
			Port:         provider.Port,
			SSL:          provider.SSL,
			SkipSSLCheck: provider.SkipSslCheck,
			User:         provider.Username,
			Pass:         provider.Password,
			ConnRetries:  provider.Retries,
			ConnWaitTime: time.Duration(provider.WaitTime) * time.Second,
			MinConns:     provider.MinConns,
			MaxConns:     provider.MaxConns,
			IdleTimeout:  time.Duration(provider.IdleTimeout) * time.Second,
		}); err != nil {
			log.Fatal(fmt.Errorf("unable to create the connection pool for provider '%s': %v", provider.Name, err))
		}

		// check the ihave and post capabilities of the provider
		if ihave, post, err := checkCapabilities(&providerList[n]); err != nil {
			log.Fatal(fmt.Errorf("unable to check capabilities of provider '%s': %v", provider.Name, err))
		} else {
			providerList[n].capabilities.ihave = ihave
			providerList[n].capabilities.post = post
			log.Printf("capabilities of '%s': ihave: %v | post: %v", provider.Name, ihave, post)
		}
	}
}

func main() {
	// loop through all file tags within the NZB file
	for _, files := range nzbfile.Files {
		// loop through all segment tags within each file tag
		for _, segment := range files.Segments {
			// prepare positiv list (for providers who have the article)
			var availableOn []*Provider
			// prepare negativ list (for providers who don't have the article)
			var missingOn []*Provider
			// loop through each provider in the provider list
			for _, provider := range providerList {
				// check if message is available on the provider
				if isAvailable, err := checkMessageID(&provider, segment.Id); err != nil {
					// error handling
					log.Print(fmt.Errorf("unable to check article <%s> on provider '%s': %v", segment.Id, provider.Name, err))
					// TODO: What do we do with such errors??
				} else {
					if isAvailable {
						// if yes add the provider to the positiv list
						availableOn = append(availableOn, &provider)
					} else {
						// if not add the provider to the negativ list
						missingOn = append(missingOn, &provider)
					}
				}
			}
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
							// send the article to the provider
							log.Printf("sending article <%s> to provider '%s'", segment.Id, provider.Name)
							err := sendArticleToProvider(provider, article)
							if err != nil {
								// error handling if sending the article was unsuccessfull
								log.Print(fmt.Errorf("error sending article <%s> to provider '%s': %v", segment.Id, provider.Name, err))
							} else {
								// handling of successfull send
								log.Printf("article <%s> successfully sent to provider '%s'", segment.Id, provider.Name)
							}
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
				log.Printf("article <%s> is available on all providers", segment.Id)
			}
		}
	}
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
