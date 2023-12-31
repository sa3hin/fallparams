package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/projectdiscovery/goflags"
	"github.com/projectdiscovery/gologger"
	"github.com/projectdiscovery/katana/pkg/engine/standard"
	"github.com/projectdiscovery/katana/pkg/output"
	"github.com/projectdiscovery/katana/pkg/types"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type arrayFlags []string

var (
	customHeaders                   arrayFlags
	sleepTime, thread               int
	headlessP, headlessC, crawlMode bool
	inputUrls, outputFile           string
	wg                              sync.WaitGroup
)

func (i *arrayFlags) Set(value string) error {
	*i = append(*i, value)
	return nil
}

func (i *arrayFlags) String() string {
	return "my string representation"
}

func main() {
	flagSet := goflags.NewFlagSet()
	flagSet.SetDescription("Find All Parameters")

	createGroup(flagSet, "input", "Input",
		flagSet.StringVarP(&inputUrls, "url", "u", "", "Input [Filename | URL]"),
	)

	createGroup(flagSet, "rate-limit", "Rate-Limit",
		flagSet.IntVarP(&thread, "thread", "t", 1, "Number Of Thread [Number]"),
		flagSet.IntVarP(&sleepTime, "sleep", "s", 0, "Time for sleep after sending each request"),
	)

	createGroup(flagSet, "configs", "Configurations",
		flagSet.BoolVarP(&crawlMode, "crawl", "c", false, "Crawl pages to extract their parameters"),
		flagSet.BoolVarP(&headlessC, "headless-crawl", "hc", false, "Enable headless hybrid crawling (experimental)"),
		flagSet.BoolVarP(&headlessP, "headless-parameter", "hp", false, "Discover parameters with headless browser"),
		flagSet.VarP(&customHeaders, "header", "H", "Header `\"Name: Value\"`, separated by colon. Multiple -H flags are accepted."),
	)

	createGroup(flagSet, "output", "Output",
		flagSet.StringVarP(&outputFile, "output", "o", "parameters.txt", "File to write output to"),
	)
	_ = flagSet.Parse()
	if inputUrls == "" {
		gologger.Error().Msg("Input is empty!\n")
		gologger.Info().Msg("Use -h flag for help.\n\n")
		os.Exit(1)
	}

	allUrls := readInput(inputUrls)
	if crawlMode {
		for _, v := range allUrls {
			allUrls = append(allUrls, simpleCrawl(v, sleepTime, headlessC)...)
		}
	}

	_, _ = os.Create(outputFile)
	allUrls = unique(allUrls)

	channel := make(chan string, len(allUrls))
	for _, myLink := range allUrls {
		channel <- myLink
	}
	close(channel)

	for i := 0; i < thread; i++ {
		wg.Add(1)
		go showResult(channel)
	}
	wg.Wait()
}

func readInput(input string) []string {
	if IsUrl(input) {
		return []string{input}
	}
	fileByte, err := os.ReadFile(input)
	checkError(err)
	return strings.Split(string(fileByte), "\n")
}

func IsUrl(str string) bool {
	u, err := url.Parse(str)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func simpleCrawl(link string, delay int, headlessMode bool) []string {
	var allLinks []string
	options := &types.Options{
		MaxDepth:               2, // Maximum depth to crawl
		ScrapeJSResponses:      true,
		ScrapeJSLuiceResponses: false,
		CrawlDuration:          0,
		Timeout:                10,
		Headless:               headlessMode,
		UseInstalledChrome:     false,
		ShowBrowser:            false,
		HeadlessNoSandbox:      true,
		HeadlessNoIncognito:    false,
		Scope:                  nil,
		OutOfScope:             nil,
		Delay:                  delay,
		NoScope:                false,
		DisplayOutScope:        false,
		OutputMatchRegex:       nil,
		OutputFilterRegex:      nil,
		KnownFiles:             "all",
		ExtensionsMatch:        nil,
		ExtensionFilter:        nil,
		Silent:                 true,
		OutputMatchCondition:   "NOTANYMATCHCONDITION",
		FieldScope:             "rdn",           // Crawling Scope Field
		BodyReadSize:           2 * 1024 * 1024, // Maximum response size to read
		RateLimit:              150,             // Maximum requests to send per second
		Strategy:               "depth-first",   // Visit strategy (depth-first, breadth-first)
		OnResult: func(result output.Result) { // Callback function to execute for result
			allLinks = append(allLinks, result.Request.URL)
		},
	}
	crawlerOptions, err := types.NewCrawlerOptions(options)
	if err != nil {
		gologger.Fatal().Msg(err.Error())
	}
	defer crawlerOptions.Close()
	crawler, err := standard.New(crawlerOptions)
	if err != nil {
		gologger.Fatal().Msg(err.Error())
	}
	defer crawler.Close()
	err = crawler.Crawl(link)
	if err != nil {
		gologger.Warning().Msgf("Could not crawl %s: %s", link, err.Error())
	}
	defer gologger.Info().Msg(fmt.Sprintf("%d endpoints were found by crawling\n\n", len(allLinks)))
	return allLinks
}

func sendRequest(link string) (*http.Response, string) {
	client := &http.Client{
		Timeout: 60 * time.Second,
	}
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return nil, "temp"
	}

	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Referer", link)

	if len(customHeaders) != 0 {
		for _, v := range customHeaders {
			req.Header.Set(strings.Split(v, ":")[0], strings.Split(v, ":")[1])
		}
	}
	res, err := client.Do(req)
	checkError(err)
	var resByte []byte
	if err == nil {
		resByte, err = io.ReadAll(res.Body)
		checkError(err)
	} else {
		return nil, "temp"
	}
	if sleepTime != 0 {
		time.Sleep(time.Duration(int32(sleepTime)) * time.Second)
	}
	return res, string(resByte)
}

func headlessBrowser(link string) string {
	options := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("disable-infobars", true),
		chromedp.Flag("headless", true),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("password-store", false),
		chromedp.Flag("disable-extensions", false),
	)

	headers := map[string]interface{}{
		"User-Agent":      "Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/114.0",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.5",
		"Sec-Fetch-Dest":  "document",
		"Sec-Fetch-Mode":  "navigate",
		"Sec-Fetch-Site":  "none",
		"Sec-Fetch-User":  "?1",
		"Referer":         link,
	}
	if len(customHeaders) > 0 {
		for _, head := range customHeaders {
			key := strings.Split(head, ":")[0]
			value := strings.Split(head, ":")[1][1:]
			headers[key] = value
		}
	}

	// Create a new context
	allocContext, _ := chromedp.NewExecAllocator(context.Background(), options...)
	ctx, cancel := chromedp.NewContext(allocContext)
	defer cancel()

	// Set up network interception to add custom headers
	err := chromedp.Run(ctx, network.Enable(), network.SetExtraHTTPHeaders(headers))
	checkError(err)

	// Navigate to the URL and retrieve the page DOM
	var htmlContent string
	err = chromedp.Run(ctx,
		chromedp.Navigate(link),
		chromedp.WaitReady("body"),
		chromedp.OuterHTML("html", &htmlContent),
	)

	checkError(err)
	return htmlContent
}

func myRegex(myRegex string, response string, indexes []int) []string {
	r, e := regexp.Compile(myRegex)
	checkError(e)
	allName := r.FindAllStringSubmatch(response, -1)
	var finalResult []string
	for _, index := range indexes {
		for _, v := range allName {
			if v[index] != "" {
				finalResult = append(finalResult, v[index])
			}
		}
	}
	return finalResult
}

func findParameter(link string) []string {
	var allParameter []string
	var result []string
	if IsUrl(link) {
		gologger.Info().Msg("Started parameter discovery for => " + link + "\n")
		body := ""
		httpRes := &http.Response{}
		if !headlessP {
			httpRes, body = sendRequest(link)
		} else {
			body = headlessBrowser(link)
		}
		cnHeader := strings.ToLower(httpRes.Header.Get("Content-Type"))

		// Get parameter from url
		linkParameter := queryStringKey(link)
		allParameter = append(allParameter, linkParameter...)

		// Variable Name
		variableNamesRegex := myRegex(`(let|const|var)\s([\w\,\s]+)\s*?(\n|\r|;|=)`, body, []int{2})
		var variableNames []string
		for _, v := range variableNamesRegex {
			for _, j := range strings.Split(v, ",") {
				variableNames = append(variableNames, strings.Replace(j, " ", "", -1))
			}
		}
		allParameter = append(allParameter, variableNames...)

		// Json and Object keys
		jsonObjectKey := myRegex(`["|']([\w\-]+)["|']\s*?:`, body, []int{1})
		allParameter = append(allParameter, jsonObjectKey...)

		// String format variable
		stringFormat := myRegex(`\${(\s*[\w\-]+)\s*}`, body, []int{1})
		allParameter = append(allParameter, stringFormat...)

		// Function input
		funcInput := myRegex(`.*\(\s*["|']?([\w\-]+)["|']?\s*(\,\s*["|']?([\w\-]+)["|']?\s*)?(\,\s*["|']?([\w\-]+)["|']?\s*)?\)`, body, []int{1, 3, 5})
		allParameter = append(allParameter, funcInput...)

		// Path Input
		pathInput := myRegex(`\/\{(.*)\}`, body, []int{1})
		allParameter = append(allParameter, pathInput...)

		// Query string key in source
		queryString := myRegex(`(\?([\w\-]+)=)|(\&([\w\-]+)=)`, body, []int{2, 4})
		allParameter = append(allParameter, queryString...)

		if cnHeader != "application/javascript" {
			// Name HTML attribute
			inputName := myRegex(`name\s*?=\s*?["|']([\w\-]+)["|']`, body, []int{1})
			allParameter = append(allParameter, inputName...)

			// ID HTML attribute
			htmlID := myRegex(`id\s*=\s*["|']([\w\-]+)["|']`, body, []int{1})
			allParameter = append(allParameter, htmlID...)
		}

		// XML attributes
		if strings.Contains(cnHeader, "xml") {
			xmlAtr := myRegex(`<([a-zA-Z0-9$_\.-]*?)>`, body, []int{1})
			allParameter = append(allParameter, xmlAtr...)
		}
	}
	for _, v := range allParameter {
		if v != "" {
			result = append(result, v)
		}
	}
	defer gologger.Info().Msg(fmt.Sprintf("%d parameters were found\n\n", len(result)))
	return result
}

func queryStringKey(link string) []string {
	u, e := url.Parse(link)
	checkError(e)
	var keys []string
	for _, v := range strings.Split(u.RawQuery, "&") {
		keys = append(keys, strings.Split(v, "=")[0])
	}
	return keys
}

func unique(strSlice []string) []string {
	keys := make(map[string]bool)
	var list []string
	for _, entry := range strSlice {
		if _, value := keys[entry]; !value {
			keys[entry] = true
			list = append(list, entry)
		}
	}
	return list
}

func showResult(channel chan string) {
	defer wg.Done()
	for v := range channel {
		for _, i := range unique(findParameter(v)) {
			file, err := os.OpenFile(outputFile, os.O_APPEND|os.O_WRONLY, 0666)
			checkError(err)
			_, err = fmt.Fprintln(file, i)
			checkError(err)
			err = file.Close()
			checkError(err)
		}
	}
}

func createGroup(flagSet *goflags.FlagSet, groupName, description string, flags ...*goflags.FlagData) {
	flagSet.SetGroup(groupName, description)
	for _, currentFlag := range flags {
		currentFlag.Group(groupName)
	}
}

func checkError(e error) {
	if e != nil {
		fmt.Println(e.Error())

	}
}