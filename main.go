package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

var (
	client             = http.Client{Timeout: 10 * time.Second}
	ErrFailToParseHTML = errors.New("could not parse HTML")
)

func fetch(url string) ([]byte, []string, error) {
	resp, err := client.Get(url)
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
		return body, nil, fmt.Errorf("%w: %w", ErrFailToParseHTML, err)
	}

	urls := []string{}

	doc.Find("a[href]").Each(func(index int, item *goquery.Selection) {
		href, _ := item.Attr("href")
		if !strings.HasPrefix(href, "mailto:") {
			urls = append(urls, href)
		}
	})

	doc.Find("img[src]").Each(func(index int, item *goquery.Selection) {
		src, _ := item.Attr("src")
		urls = append(urls, src)
	})

	return body, urls, nil
}

func urlToPath(u string) (string, error) {
	u2, err := url.Parse(u)
	if err != nil {
		return "", err
	}

	path := u2.Path

	// detect if we're downloading from a root or a directory
	// and if so, save contents as index.html
	if len(path) == 0 || path[len(path)-1] == '/' {
		path = filepath.Join(path, "index.html")
	}

	// rebase against a faux root directory to remove any relative paths
	root := url.URL{Path: "/"}
	canonical, err := root.Parse(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not canonicalise: %v\n", err)
		return "", err
	}

	// we will treat query parameters as potential new files
	// that can be fetched from the filesystem
	if u2.RawQuery != "" {
		return canonical.Path + "?" + u2.RawQuery, nil
	}

	return canonical.Path, nil
}

// listFlags is an implementation of the flag.Value interface
type listFlags []string

func (l *listFlags) String() string {
	return fmt.Sprintf("%v", *l)
}

func (l *listFlags) Set(value string) error {
	*l = append(*l, value)
	return nil
}

func main() {
	var depth uint
	var includes listFlags
	var excludes listFlags
	var refresh listFlags

	flag.UintVar(&depth, "depth", math.MaxUint, "depth for recursion")
	flag.Var(&includes, "include", `regex(es) of URLs limiting what to include when downloading, e.g. -include blog.cr.yp.to/(.*html|.*jpg)$ [default: ".*"]`)
	flag.Var(&excludes, "exclude", "regex(es) of URLs of what not to include when downloading, e.g. -exclude blog.cr.yp.to/.*js$")
	flag.Var(&refresh, "refresh", "regex(es) of URLs of what should always be redownloaded, e.g. -refresh blog.cr.yp.to/.*sha256sum$")

	flag.Parse()

	args := flag.Args()

	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "./mrdriller [-depth #] [-include regex1 -include regex2 ...] [-exclude regex1 -exclude regex2 ...] [-refresh regex1 -refresh regex2 ...] URL")
		os.Exit(1)
	}

	if len(includes) == 0 {
		includes = []string{".*"}
	}

	includeRE := []*regexp.Regexp{}
	excludeRE := []*regexp.Regexp{}
	refreshRE := []*regexp.Regexp{}

	for _, r := range includes {
		c, err := regexp.Compile(r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to compile regexp `%s`: %v", r, err)
			os.Exit(1)
		}

		includeRE = append(includeRE, c)
	}

	for _, r := range excludes {
		c, err := regexp.Compile(r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to compile regexp `%s`: %v", r, err)
			os.Exit(1)
		}

		excludeRE = append(excludeRE, c)
	}

	for _, r := range refresh {
		c, err := regexp.Compile(r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to compile regexp `%s`: %v", r, err)
			os.Exit(1)
		}

		refreshRE = append(refreshRE, c)
	}

	fmt.Printf("Depth is: %d\n", depth)
	fmt.Printf("Includes is: %#v\n", includes)
	fmt.Printf("Excludes is: %#v\n", excludes)
	fmt.Printf("Refresh is: %#v\n", refresh)

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

	type Item struct {
		url   string
		depth uint
	}

	queue := []Item{{args[0], 0}}
	host := strings.ToLower(u.Host)
	scheme := u.Scheme

	seen := map[string]struct{}{}

	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]

		if i.depth > depth {
			fmt.Printf("skipping %s exceeds depth limit\n", i.url)
			continue
		}

		if _, ok := seen[i.url]; ok {
			continue
		}

		// First we check excludes for any match to see if we shouldn't
		// be downloading this URL, skip if we shouldn't.
		// Then we check includes to see if any match, and if it does
		// then we download the file, otherwise skip.

		matched := false

		for _, re := range excludeRE {
			if re.MatchString(i.url) {
				matched = true
				break
			}
		}

		if matched {
			seen[i.url] = struct{}{}
			continue
		}

		for _, re := range includeRE {
			if re.MatchString(i.url) {
				matched = true
				break
			}
		}

		if !matched {
			seen[i.url] = struct{}{}
			continue
		}

		path, err := urlToPath(i.url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning, could not convert url %s to local path: %v\n", i.url, err)
			continue
		}

		// directories are laid out as "https:my.web.site:80"
		// port is omitted if omitted in input URL
		// (no credentials are stored in the name)
		path = filepath.Join(dir, u.Scheme+":"+strings.ToLower(u.Host), path)

		var info os.FileInfo

		for _, re := range refreshRE {
			if re.MatchString(i.url) {
				goto fetch
			}
		}

		info, err = os.Stat(path)

		if err == nil {
			size := info.Size()

			resp, err := client.Head(i.url)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning, could not HEAD url %s: %v", i.url, err)
				continue
			}

			resp.Body.Close()

			lengthStr := resp.Header.Get("Content-Length")

			if lengthStr != "" {
				length, err := strconv.Atoi(lengthStr)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning, content-length string is not an integer (got %s), force downloading", lengthStr)
				} else if int64(length) == size {
					// file on filesystem same size as remote,
					// then assume we've already fetched correctly
					continue
				}
			}
		}

	fetch:
		b, hrefs, err := fetch(i.url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning, couldn't process URL %s: %v\n", i.url, err)

			if errors.Is(err, ErrFailToParseHTML) {
				goto save
			}

			continue
		}

		for _, link := range hrefs {
			u, err := url.Parse(link)
			if err != nil {
				fmt.Fprintf(os.Stderr, "(skipping) could not parse URL %s\n", link)
				continue
			}

			if u.Host != "" && strings.ToLower(u.Host) != host {
				continue
			}

			if u.Host == "" {
				u.Host = host
				u.Scheme = scheme

				// Here's where it gets tricky, we need to join i.url
				// with the relative path given by u.Path, for example:
				//   https://foo, bar.html -> https://foo/bar.html
				//   https://foo/index.html, bar.html -> https://foo/bar.html
				//   https://foo/a/index.html, bar.html -> https://foo/a/bar.html
				//   etc.
				if u.Path != "" && u.Path[0] != '/' {
					base, err := url.Parse(i.url)
					if err != nil {
						fmt.Fprintf(os.Stderr, "(skipping) could not parse base URL %s [%s]\n", i.url, link)
						continue
					}

					base, err = base.Parse(u.Path)
					if err != nil {
						fmt.Fprintf(os.Stderr, "(skipping) failed to rebase URL %s [%s]\n", i.url, link)
					}

					u.Path = base.Path
				}
			}

			// we want to collapse all urls with a '#' in it
			u.Fragment = ""
			u.RawFragment = ""

			link = u.String()

			if _, ok := seen[link]; !ok {
				queue = append(queue, Item{link, i.depth + 1})
			}
		}

	save:

		writeDir := filepath.Dir(path)
		err = os.MkdirAll(writeDir, 0755)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning, could not create directory tree %s: %v\n", writeDir, err)
			continue
		}

		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning, could not save file %s: %v\n", path, err)
			continue
		}

		f.Write(b)
		f.Close()

		seen[i.url] = struct{}{}
		fmt.Fprintf(os.Stderr, "Got %s -> %s\n", i.url, path)
	}
}
