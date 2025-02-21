package prompt

import "github.com/openGemini/go-prompt"

type Completer struct {
}

func NewCompleter() *Completer {
	return &Completer{}
}

func (c *Completer) completer(d prompt.Document) []prompt.Suggest {
	return []prompt.Suggest{}
}
