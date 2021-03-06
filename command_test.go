package jasper

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mongodb/grip/level"
	"github.com/mongodb/grip/send"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	echo, ls         = "echo", "ls"
	arg1, arg2, arg3 = "ZXZlcmdyZWVu", "aXM=", "c28gY29vbCE="
	lsErrorMsg       = "No such file or directory"
)

type Buffer struct {
	b bytes.Buffer
	sync.RWMutex
}

func (b *Buffer) Read(p []byte) (n int, err error) {
	b.RLock()
	defer b.RUnlock()
	return b.b.Read(p)
}
func (b *Buffer) Write(p []byte) (n int, err error) {
	b.Lock()
	defer b.Unlock()
	return b.b.Write(p)
}
func (b *Buffer) String() string {
	b.RLock()
	defer b.RUnlock()
	return b.b.String()
}

func (b *Buffer) Close() error { return nil }

func verifyCommandAndGetOutput(ctx context.Context, t *testing.T, cmd *Command, run cmdRunFunc, success bool) string {
	var buf bytes.Buffer
	bufCloser := &Buffer{b: buf}

	cmd.SetCombinedWriter(bufCloser)

	if success {
		assert.NoError(t, run(cmd, ctx))
	} else {
		assert.Error(t, run(cmd, ctx))
	}

	return bufCloser.String()
}

func checkOutput(t *testing.T, exists bool, output string, expectedOutputs ...string) {
	for _, expected := range expectedOutputs {
		// TODO: Maybe don't try to be so cheeky with an XOR...
		assert.True(t, exists == strings.Contains(output, expected))
	}
}

type cmdRunFunc func(*Command, context.Context) error

func TestCommandImplementation(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	for runFuncType, runFunc := range map[string]cmdRunFunc{
		"NonParallel": (*Command).Run,
		"Parallel":    (*Command).RunParallel,
	} {
		t.Run(runFuncType, func(t *testing.T) {
			for name, testCase := range map[string]func(context.Context, *testing.T){
				"ValidRunCommandDoesNotError": func(ctx context.Context, t *testing.T) {
					cmd := NewCommand().ID(t.Name()).Priority(level.Info).Add(
						[]string{echo, arg1},
					).Directory(cwd)
					assert.NoError(t, runFunc(cmd, ctx))
				},
				"UnsuccessfulRunCommandErrors": func(ctx context.Context, t *testing.T) {
					cmd := NewCommand().ID(t.Name()).Priority(level.Info).Add(
						[]string{ls, arg2},
					).Directory(cwd)
					err := runFunc(cmd, ctx)
					assert.Error(t, err)
					assert.True(t, strings.Contains(err.Error(), "exit status"))
				},
				"InvalidArgsCommandErrors": func(ctx context.Context, t *testing.T) {
					cmd := NewCommand().Add([]string{})
					assert.EqualError(t, runFunc(cmd, ctx), "args invalid")
				},
				"PreconditionDeterminesExecution": func(ctx context.Context, t *testing.T) {
					for _, precondition := range []func() bool{
						func() bool {
							return true
						},
						func() bool {
							return false
						},
					} {
						t.Run(fmt.Sprintf("%tPrecondition", precondition()), func(t *testing.T) {
							cmd := NewCommand().Prerequisite(precondition).Add([]string{echo, arg1})
							output := verifyCommandAndGetOutput(ctx, t, cmd, runFunc, true)
							checkOutput(t, precondition(), output, arg1)
						})
					}
				},
				"SingleInvalidSubCommandCausesTotalError": func(ctx context.Context, t *testing.T) {
					cmd := NewCommand().ID(t.Name()).Priority(level.Info).Extend(
						[][]string{
							[]string{echo, arg1},
							[]string{ls, arg2},
							[]string{echo, arg3},
						},
					).Directory(cwd)
					assert.Error(t, runFunc(cmd, ctx))
				},
				"ExecutionFlags": func(ctx context.Context, t *testing.T) {
					numCombinations := int(math.Pow(2, 3))
					for i := 0; i < numCombinations; i++ {
						continueOnError, ignoreError, includeBadCmd := i&1 == 1, i&2 == 2, i&4 == 4

						cmd := NewCommand().Add([]string{echo, arg1})
						if includeBadCmd {
							cmd.Add([]string{ls, arg3})
						}
						cmd.Add([]string{echo, arg2})

						subTestName := fmt.Sprintf(
							"ContinueOnErrorIs%tAndIgnoreErrorIs%tAndIncludeBadCmdIs%t",
							continueOnError,
							ignoreError,
							includeBadCmd,
						)
						t.Run(subTestName, func(t *testing.T) {
							if runFuncType == "Parallel" && !continueOnError {
								t.Skip("Continue on error only applies to non parallel executions")
							}
							cmd.ContinueOnError(continueOnError)
							cmd.IgnoreError(ignoreError)
							successful := ignoreError || !includeBadCmd
							outputAfterLsExists := !includeBadCmd || continueOnError
							output := verifyCommandAndGetOutput(ctx, t, cmd, runFunc, successful)
							checkOutput(t, true, output, arg1)
							checkOutput(t, includeBadCmd, output, lsErrorMsg)
							checkOutput(t, outputAfterLsExists, output, arg2)
						})
					}
				},
				"CommandOutputAndErrorIsReadable": func(ctx context.Context, t *testing.T) {
					for subName, subTestCase := range map[string]func(context.Context, *testing.T, *Command, cmdRunFunc){
						"StdOutOnly": func(ctx context.Context, t *testing.T, cmd *Command, run cmdRunFunc) {
							cmd.Add([]string{echo, arg1})
							cmd.Add([]string{echo, arg2})
							output := verifyCommandAndGetOutput(ctx, t, cmd, run, true)
							checkOutput(t, true, output, arg1, arg2)
						},
						"StdErrOnly": func(ctx context.Context, t *testing.T, cmd *Command, run cmdRunFunc) {
							cmd.Add([]string{ls, arg3})
							output := verifyCommandAndGetOutput(ctx, t, cmd, run, false)
							checkOutput(t, true, output, lsErrorMsg)
						},
						"StdOutAndStdErr": func(ctx context.Context, t *testing.T, cmd *Command, run cmdRunFunc) {
							cmd.Add([]string{echo, arg1})
							cmd.Add([]string{echo, arg2})
							cmd.Add([]string{ls, arg3})
							output := verifyCommandAndGetOutput(ctx, t, cmd, run, false)
							checkOutput(t, true, output, arg1, arg2, lsErrorMsg)
						},
					} {
						t.Run(subName, func(t *testing.T) {
							cmd := NewCommand()
							subTestCase(ctx, t, cmd, runFunc)
						})
					}
				},
				"WriterOutputAndErrorIsSettable": func(ctx context.Context, t *testing.T) {
					for subName, subTestCase := range map[string]func(context.Context, *testing.T, *Command, *Buffer, cmdRunFunc){
						"StdOutOnly": func(ctx context.Context, t *testing.T, cmd *Command, buf *Buffer, run cmdRunFunc) {
							cmd.SetOutputWriter(buf)
							require.NoError(t, run(cmd, ctx))
							checkOutput(t, true, buf.String(), arg1, arg2)
							checkOutput(t, false, buf.String(), lsErrorMsg)
						},
						"StdErrOnly": func(ctx context.Context, t *testing.T, cmd *Command, buf *Buffer, run cmdRunFunc) {
							cmd.SetErrorWriter(buf)
							require.NoError(t, run(cmd, ctx))
							checkOutput(t, true, buf.String(), lsErrorMsg)
							checkOutput(t, false, buf.String(), arg1, arg2)
						},
						"StdOutAndStdErr": func(ctx context.Context, t *testing.T, cmd *Command, buf *Buffer, run cmdRunFunc) {
							cmd.SetCombinedWriter(buf)
							require.NoError(t, run(cmd, ctx))
							checkOutput(t, true, buf.String(), arg1, arg2, lsErrorMsg)
						},
					} {
						t.Run(subName, func(t *testing.T) {
							cmd := NewCommand().Extend([][]string{
								[]string{echo, arg1},
								[]string{echo, arg2},
								[]string{ls, arg3},
							}).ContinueOnError(true).IgnoreError(true)

							var buf bytes.Buffer
							bufCloser := &Buffer{b: buf}

							subTestCase(ctx, t, cmd, bufCloser, runFunc)
						})
					}
				},
				"SenderOutputAndErrorIsSettable": func(ctx context.Context, t *testing.T) {
					for subName, subTestCase := range map[string]func(context.Context, *testing.T, *Command, *send.InMemorySender, cmdRunFunc){
						"StdOutOnly": func(ctx context.Context, t *testing.T, cmd *Command, sender *send.InMemorySender, run cmdRunFunc) {
							cmd.SetOutputSender(cmd.priority, sender)
							require.NoError(t, run(cmd, ctx))
							out, err := sender.GetString()
							require.NoError(t, err)
							checkOutput(t, true, strings.Join(out, "\n"), "[p=info]:", arg1, arg2)
							checkOutput(t, false, strings.Join(out, "\n"), lsErrorMsg)
						},
						"StdErrOnly": func(ctx context.Context, t *testing.T, cmd *Command, sender *send.InMemorySender, run cmdRunFunc) {
							cmd.SetErrorSender(cmd.priority, sender)
							require.NoError(t, run(cmd, ctx))
							out, err := sender.GetString()
							require.NoError(t, err)
							checkOutput(t, true, strings.Join(out, "\n"), "[p=info]:", lsErrorMsg)
							checkOutput(t, false, strings.Join(out, "\n"), arg1, arg2)
						},
						"StdOutAndStdErr": func(ctx context.Context, t *testing.T, cmd *Command, sender *send.InMemorySender, run cmdRunFunc) {
							cmd.SetCombinedSender(cmd.priority, sender)
							require.NoError(t, run(cmd, ctx))
							out, err := sender.GetString()
							require.NoError(t, err)
							checkOutput(t, true, strings.Join(out, "\n"), "[p=info]:", arg1, arg2, lsErrorMsg)
						},
					} {
						t.Run(subName, func(t *testing.T) {
							cmd := NewCommand().Extend([][]string{
								[]string{echo, arg1},
								[]string{echo, arg2},
								[]string{ls, arg3},
							}).ContinueOnError(true).IgnoreError(true).Priority(level.Info)

							levelInfo := send.LevelInfo{Default: cmd.priority, Threshold: cmd.priority}
							sender, err := send.NewInMemorySender(t.Name(), levelInfo, 100)
							require.NoError(t, err)

							subTestCase(ctx, t, cmd, sender.(*send.InMemorySender), runFunc)
						})
					}
				},
				// "": func(ctx context.Context, t *testing.T) {},
			} {
				t.Run(name, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), taskTimeout)
					defer cancel()

					testCase(ctx, t)
				})
			}
		})
	}
}

func TestRunParallelRunsInParallel(t *testing.T) {
	cmd := NewCommand().Extend([][]string{
		[]string{"sleep", "3"},
		[]string{"sleep", "3"},
		[]string{"sleep", "3"},
	})
	threePointOneSeconds := time.Second*3 + time.Millisecond*100
	maxRunTimeAllowed := threePointOneSeconds
	cctx, cancel := context.WithTimeout(context.Background(), maxRunTimeAllowed)
	defer cancel()
	// If this does not run in parallel, the context will timeout and we will
	// get an error.
	assert.NoError(t, cmd.RunParallel(cctx))
}
