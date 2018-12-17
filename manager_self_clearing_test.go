package jasper

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (m *selfClearingProcessManager) registerBasedCreate(ctx context.Context, t *testing.T, opts *CreateOptions) (Process, error) {
	sleep, err := newBlockingProcess(ctx, sleepCreateOpts(10))
	require.NoError(t, err)
	require.NotNil(t, sleep)
	err = m.Register(ctx, sleep)
	if err != nil {
		// Mimic the behavior of Create()'s error return.
		return nil, err
	}

	return sleep, err
}

func (m *selfClearingProcessManager) pureCreate(ctx context.Context, t *testing.T, opts *CreateOptions) (Process, error) {
	return m.Create(ctx, opts)
}

func fillUp(ctx context.Context, t *testing.T, manager *selfClearingProcessManager, numProcs int) {
	procs, err := createProcs(ctx, sleepCreateOpts(5), manager, numProcs)
	require.NoError(t, err)
	require.Len(t, procs, numProcs)
}

func TestSelfClearingManager(t *testing.T) {
	for mname, createFunc := range map[string]func(*selfClearingProcessManager, context.Context, *testing.T, *CreateOptions) (Process, error){
		"Create":   (*selfClearingProcessManager).pureCreate,
		"Register": (*selfClearingProcessManager).registerBasedCreate,
	} {
		t.Run(mname, func(t *testing.T) {
			for name, test := range map[string]func(context.Context, *testing.T, *selfClearingProcessManager){
				"SucceedsWhenFree": func(ctx context.Context, t *testing.T, manager *selfClearingProcessManager) {
					proc, err := createFunc(manager, ctx, t, trueCreateOpts())
					assert.NoError(t, err)
					assert.NotNil(t, proc)
				},
				"ErrorsWhenFull": func(ctx context.Context, t *testing.T, manager *selfClearingProcessManager) {
					fillUp(ctx, t, manager, manager.maxProcs)
					sleep, err := createFunc(manager, ctx, t, sleepCreateOpts(10))
					assert.Error(t, err)
					assert.Nil(t, sleep)
				},
				"PartiallySucceedsWhenAlmostFull": func(ctx context.Context, t *testing.T, manager *selfClearingProcessManager) {
					fillUp(ctx, t, manager, manager.maxProcs-1)
					firstSleep, err := createFunc(manager, ctx, t, sleepCreateOpts(10))
					assert.NoError(t, err)
					assert.NotNil(t, firstSleep)
					secondSleep, err := createFunc(manager, ctx, t, sleepCreateOpts(10))
					assert.Error(t, err)
					assert.Nil(t, secondSleep)
				},
				"InitialFailureIsResolvedByWaiting": func(ctx context.Context, t *testing.T, manager *selfClearingProcessManager) {
					fillUp(ctx, t, manager, manager.maxProcs)
					sleepOpts := sleepCreateOpts(100)
					sleepProc, err := createFunc(manager, ctx, t, sleepOpts)
					assert.Error(t, err)
					assert.Nil(t, sleepProc)
					otherSleepProcs, err := manager.List(ctx, All)
					require.NoError(t, err)
					for _, otherSleepProc := range otherSleepProcs {
						require.NoError(t, otherSleepProc.Wait(ctx))
					}
					sleepProc, err = createFunc(manager, ctx, t, sleepOpts)
					assert.NoError(t, err)
					assert.NotNil(t, sleepProc)
				},
				//"": func(ctx context.Context, t *testing.T, manager *selfClearingProcessManager) {},
			} {
				t.Run(name, func(t *testing.T) {
					tctx, cancel := context.WithTimeout(context.Background(), managerTestTimeout)
					defer cancel()

					selfClearingManager := NewSelfClearingProcessManager(5).(*selfClearingProcessManager)
					test(tctx, t, selfClearingManager)
					selfClearingManager.Close(tctx)
				})
			}
		})
	}
}
