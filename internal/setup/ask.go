package setup

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/tyler-johnson/jog/internal/install"
)

// readAnswer prints a one-line question and reads the reply: empty
// takes the default, y/n (any case, any suffix) decide, anything else
// asks again. ok=false means stdin closed with no usable answer — the
// caller stops asking. No TTY is required: answers pipe in fine, which
// is also how the tests drive it.
func readAnswer(r *bufio.Reader, prompt string, def bool) (answer, ok bool) {
	suffix := " [Y/n] "
	if !def {
		suffix = " [y/N] "
	}
	for {
		fmt.Print(prompt + install.StyleDim.Render(suffix))
		line, err := r.ReadString('\n')
		reply := strings.ToLower(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(reply, "y"):
			return true, true
		case strings.HasPrefix(reply, "n"):
			return false, true
		case reply == "" && err == nil:
			return def, true
		}
		if err != nil {
			fmt.Println()
			return false, false
		}
	}
}
