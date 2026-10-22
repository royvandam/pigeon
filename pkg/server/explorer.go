package server

import (
	"context"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/royvandam/pigeon/internal/size"
	"github.com/royvandam/pigeon/pkg/files"
)

type fileEntry struct {
	Name        string
	Path        string
	Directory   bool
	Size        string
	Modified    string
	BrowseURL   string
	DownloadURL string
}

type breadcrumb struct {
	Name string
	URL  string
}

type explorerData struct {
	Directory   string
	Entries     []fileEntry
	Breadcrumbs []breadcrumb
	CSRFToken   string
}

func explorerURL(directory string) string {
	if directory == "" {
		return "/"
	}
	return "/?" + url.Values{"dir": {directory}}.Encode()
}

func readExplorer(ctx context.Context, access *files.Access, directory, csrf string) (explorerData, error) {
	data := explorerData{Directory: directory, CSRFToken: csrf}
	items, err := access.List(ctx, directory)
	if err != nil {
		return data, err
	}
	for _, info := range items {
		name := path.Join(directory, info.Name())
		sizeText := "—"
		if !info.IsDir() {
			sizeText = size.Format(info.Size())
		}
		data.Entries = append(data.Entries, fileEntry{
			Name: info.Name(), Path: name, Directory: info.IsDir(), Size: sizeText,
			Modified:    info.ModTime().Local().Format("2006-01-02 15:04"),
			BrowseURL:   explorerURL(name),
			DownloadURL: "/download?" + url.Values{"path": {name}}.Encode(),
		})
	}
	sort.Slice(data.Entries, func(i, j int) bool {
		a, b := data.Entries[i], data.Entries[j]
		if a.Directory != b.Directory {
			return a.Directory
		}
		if strings.ToLower(a.Name) == strings.ToLower(b.Name) {
			return a.Name < b.Name
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	data.Breadcrumbs = append(data.Breadcrumbs, breadcrumb{Name: "My files", URL: "/"})
	if directory != "" {
		prefix := ""
		for _, component := range strings.Split(directory, "/") {
			prefix = path.Join(prefix, component)
			data.Breadcrumbs = append(data.Breadcrumbs, breadcrumb{Name: component, URL: explorerURL(prefix)})
		}
	}
	return data, nil
}
