package prompt

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/openGemini/go-prompt"

	"github.com/openGemini/openGemini-cli/internal/common"
)

type Prompt struct {
	completer *Completer
	instance  *prompt.Prompt
}

func NewPrompt(executor prompt.Executor) *Prompt {
	var completer = NewCompleter()
	var p = &Prompt{completer: completer}
	var instance = prompt.New(
		executor,
		completer.completer,
		prompt.OptionTitle("openGemini: interactive openGemini client"),
		prompt.OptionPrefix("> "),
		prompt.OptionPrefixTextColor(prompt.DefaultColor),
		prompt.OptionCompletionWordSeparator(string([]byte{' ', os.PathSeparator})),
		prompt.OptionAddASCIICodeBind(
			prompt.ASCIICodeBind{
				ASCIICode: []byte{0x1b, 0x62},
				Fn:        prompt.GoLeftWord,
			},
			prompt.ASCIICodeBind{
				ASCIICode: []byte{0x1b, 0x66},
				Fn:        prompt.GoRightWord,
			},
		),
		prompt.OptionAddKeyBind(
			prompt.KeyBind{
				Key: prompt.ShiftLeft,
				Fn:  prompt.GoLeftWord,
			},
			prompt.KeyBind{
				Key: prompt.ShiftRight,
				Fn:  prompt.GoRightWord,
			},
			prompt.KeyBind{
				Key: prompt.ControlC,
				Fn:  p.Destruction,
			},
		),
	)
	p.instance = instance
	return p
}

func (p *Prompt) Run() {
	fmt.Printf("openGemini CLI %s (rev-%s)\n", common.Version, "revision")
	fmt.Println("Please use `quit`, `exit` or `Ctrl-D` to exit this program.")
	defer p.Destruction(nil)
	p.instance.Run()
}

func (p *Prompt) Destruction(_ *prompt.Buffer) {
	if runtime.GOOS != "windows" {
		reset := exec.Command("stty", "-raw", "echo")
		reset.Stdin = os.Stdin
		_ = reset.Run()
	}
	os.Exit(0)
}
