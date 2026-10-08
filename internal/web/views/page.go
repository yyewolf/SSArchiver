// Package views holds the templ pages and partials.
package views

import "github.com/yyewolf/ssarchiver/internal/model"

type Page struct {
	Title    string
	Instance string
	Path     string
	Version  string
	User     *model.User
	OG       *OpenGraph
}

type OpenGraph struct {
	Title, Description, Image, URL string
}

func (p Page) FullTitle() string {
	if p.Title == "" {
		return p.Instance
	}
	return p.Title + " · " + p.Instance
}

const htmxConfig = `{"includeIndicatorStyles":false,"allowEval":false,"allowScriptTags":false,"historyCacheSize":0}`
