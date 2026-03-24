package agent

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func scanStreamJSONLines(r io.Reader, output io.Writer, handle func(string) error) error {
	br := bufio.NewReader(r)
	sw, ok := output.(*syncWriter)
	if !ok {
		sw = newSyncWriter(output)
	}

	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("read stream: %w", err)
		}

		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if sw != nil {
				_, _ = sw.Write([]byte(trimmed + "\n"))
			}
			if err := handle(trimmed); err != nil {
				return err
			}
		}

		if err == io.EOF {
			return nil
		}
	}
}
