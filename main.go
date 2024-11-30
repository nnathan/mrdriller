package main

import (
	"bufio"
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

	"github.com/PuerkitoBio/goquery"
)

var (
	client             = http.Client{}
	ErrFailToParseHTML = errors.New("could not parse HTML")
)

// fetch is a hairy multi-pronged function that:
//
//   - resumes a GET download from a url to a destination file (using range requests)
//
//   - starts a new GET download from a url to a destination file
//
//   - if content is html, scrapes for any href/img src links and returns them
//
//     On failure cases it tries its best to download the file (in particular if trying
//     to resume), otherwise errors gracefully.
//
//     There are no retries.
func fetch(url string, dest string, resume bool) ([]string, error) {
	var f *os.File
	var info os.FileInfo
	var req *http.Request
	var resp *http.Response
	var size int64
	var destDir string
	var err error

	if !resume {
		goto dontresume
	}

	f, err = os.OpenFile(dest, os.O_RDWR, 0666)
	// if we fail to open the file, try creating anew without resume
	if err != nil {
		goto dontresume
	}

	defer f.Close()

	info, err = f.Stat()
	if err != nil {
		f.Close()
		goto dontresume
	}

	size = info.Size()

	_, err = f.Seek(size, io.SeekStart)
	if err != nil {
		f.Close()
		goto dontresume
	}

	req, err = http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create GET request: %w", err)
	}

	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", size))

	resp, err = client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to do range GET request: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// If we get a 200 then it's not partial content,
		// which means the server is not honouring the
		// range request; reset the file for full download
		_, err = f.Seek(0, io.SeekStart)
		if err != nil {
			return nil, fmt.Errorf("failed to do seek to start of file: %w", err)
		}

		err = f.Truncate(0)
		if err != nil {
			return nil, fmt.Errorf("failed to truncate file: %w", err)
		}
	} else if resp.StatusCode != http.StatusPartialContent {
		f.Close()
		goto dontresume
	}

	goto copyfile

dontresume:

	resp, err = client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("got bad http status %s", resp.Status)
	}

	destDir = filepath.Dir(dest)
	err = os.MkdirAll(destDir, 0755)
	if err != nil {
		return nil, fmt.Errorf("could not create destination directory %s: %v", destDir, err)
	}

	f, err = os.Create(dest)
	if err != nil {
		return nil, fmt.Errorf("could not create file %s: %v\n", dest, err)
	}

	defer f.Close()

copyfile:

	if _, err = io.Copy(f, resp.Body); err != nil {
		return nil, fmt.Errorf("error doing io copy: %w", err)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(contentType, "text/html") {
		return nil, nil
	}

	_, err = f.Seek(0, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("could not reread file for parsing links: %w", err)
	}

	doc, err := goquery.NewDocumentFromReader(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFailToParseHTML, err)
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

	return urls, nil
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
	var resume bool
	var depth uint
	var includes listFlags
	var excludes listFlags
	var refresh listFlags

	flag.BoolVar(&resume, "resume", false, "resume previously downloaded files")
	flag.UintVar(&depth, "depth", math.MaxUint, "depth for recursion")
	flag.Var(&includes, "include", `regex(es) of URLs limiting what to include when downloading, e.g. -include 'blog.cr.yp.to/(.*html|.*jpg)$' [default: ".*"]`)
	flag.Var(&excludes, "exclude", "regex(es) of URLs of what not to include when downloading, e.g. -exclude 'blog.cr.yp.to/.*js$'")
	flag.Var(&refresh, "refresh", "regex(es) of URLs of what should always be redownloaded, e.g. -refresh '\\.md5$'")

	flag.Parse()

	args := flag.Args()

	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "./mrdriller [-resume] [-depth #] [-include regex1 -include regex2 ...] [-exclude regex1 -exclude regex2 ...] [-refresh regex1 -refresh regex2 ...] URL")
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

		matched = false
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

		shouldResume := resume

		for _, re := range refreshRE {
			if re.MatchString(i.url) {
				shouldResume = false
				goto fetch
			}
		}

		info, err = os.Stat(path)

		if err == nil {
			localSize := info.Size()

			resp, err := client.Head(i.url)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning, could not HEAD url %s: %v", i.url, err)
				continue
			}

			resp.Body.Close()

			lengthStr := resp.Header.Get("Content-Length")

			if lengthStr != "" {
				l, err := strconv.Atoi(lengthStr)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning, content-length string is not an integer (got %s), force downloading", lengthStr)
					shouldResume = false
				} else if int64(l) == localSize {
					// file on filesystem same size as remote,
					// then assume we've already fetched correctly
					continue
				}
			}
		}

	fetch:

		hrefs, err := fetch(i.url, path, shouldResume)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning, couldn't process URL %s: %v\n", i.url, err)

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

		seen[i.url] = struct{}{}
		fmt.Fprintf(os.Stderr, "Got %s -> %s\n", i.url, path)
	}
}
