package agentlimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClassificationZeroValue(t *testing.T) {
	var c Classification
	assert := assert.New(t)
	assert.Equal(KindNone, c.Kind)
	assert.Empty(c.Agent)
	assert.True(c.ResetAt.IsZero())
	assert.Equal(time.Duration(0), c.CooldownFor)
	assert.Empty(c.Message)
}
