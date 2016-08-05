package session

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"text/scanner"

	"github.com/peterh/liner"
)

const (
	promptDefault  = "gore> "
	promptContinue = "..... "
	indent         = "    "
)

// ContLiner is a simple line editor for prompt.
type ContLiner struct {
	*liner.State
	buffer string
	depth  int
}

// NewContLiner returns a new ContLiner instance.
func NewContLiner() *ContLiner {
	rl := liner.NewLiner()
	rl.SetCtrlCAborts(true)
	return &ContLiner{State: rl}
}

func (cl *ContLiner) promptString() string {
	if cl.buffer != "" {
		return promptContinue + strings.Repeat(indent, cl.depth)
	}

	return promptDefault
}

func (cl *ContLiner) Prompt() (string, error) {
	line, err := cl.State.Prompt(cl.promptString())
	if err == io.EOF {
		if cl.buffer != "" {
			// cancel line continuation
			cl.Accepted()
			fmt.Println()
			err = nil
		}
	} else if err == liner.ErrPromptAborted {
		err = nil
		if cl.buffer != "" {
			cl.Accepted()
		} else {
			fmt.Println("(^D to quit)")
		}
	} else if err == nil {
		if cl.buffer != "" {
			cl.buffer = cl.buffer + "\n" + line
		} else {
			cl.buffer = line
		}
	}

	return cl.buffer, err
}

func (cl *ContLiner) Accepted() {
	cl.State.AppendHistory(cl.buffer)
	cl.buffer = ""
}

func (cl *ContLiner) Clear() {
	cl.buffer = ""
	cl.depth = 0
}

var errUnmatchedBraces = fmt.Errorf("unmatched braces")

func (cl *ContLiner) Reindent() error {
	oldDepth := cl.depth
	cl.depth = cl.countDepth()

	if cl.depth < 0 {
		return errUnmatchedBraces
	}

	if cl.depth < oldDepth {
		lines := strings.Split(cl.buffer, "\n")
		if len(lines) > 1 {
			lastLine := lines[len(lines)-1]

			cursorUp()
			fmt.Printf("\r%s%s", cl.promptString(), lastLine)
			eraseInLine()
			fmt.Print("\n")
		}
	}

	return nil
}

func (cl *ContLiner) countDepth() int {
	reader := bytes.NewBufferString(cl.buffer)
	sc := new(scanner.Scanner)
	sc.Init(reader)
	sc.Error = func(_ *scanner.Scanner, msg string) {
		debugf("scanner: %s", msg)
	}

	depth := 0
	for {
		switch sc.Scan() {
		case '{':
			depth++
		case '}':
			depth--
		case scanner.EOF:
			return depth
		}
	}
}
