package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func fetch(url string) ([]byte, []string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch URL: %w", err)
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read http body: %w", err)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(contentType, "text/html") {
		return body, nil, nil
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return body, nil, fmt.Errorf("failed to parse html: %w", err)
	}

	urls := []string{}

	doc.Find("a[href]").Each(func(index int, item *goquery.Selection) {
		href, _ := item.Attr("href")
		urls = append(urls, href)
	})

	return body, urls, nil
}

func main() {
	var depth uint

	flag.UintVar(&depth, "depth", math.MaxUint, "depth for recursion")

	flag.Parse()

	args := flag.Args()

	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "./mrdriller [-d #] URL")
		os.Exit(1)
	}

	fmt.Printf("Depth is: %d\n", depth)

	u, err := url.Parse(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing URL %s: %v", args[0], err)
		os.Exit(1)
	}

	if !strings.HasPrefix(u.Scheme, "http") {
		fmt.Fprintln(os.Stderr, "URL must be http or https")
		os.Exit(1)
	}

	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to get working directory: %#v\n", err)
		os.Exit(1)
	}

	// directories are laid out as "https:my.web.site:80"
	// port is omitted if omitted in input URL
	// (no credentials are stored in the name)
	hostDir := filepath.Join(dir, u.Scheme+":"+u.Host)

	fmt.Printf("%s\n", hostDir)
	fmt.Printf("%#v\n", u)

	b, urls, err := fetch(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to fetch %s: %v", args[0], err)
		os.Exit(1)
	}

	_ = b

	fmt.Printf("%#v\n", urls)
}
