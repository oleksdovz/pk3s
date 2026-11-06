package engine

import (
	"context"

	"github.com/oleksdovz/pk3s/internal/plan"
)

// The merged activities shown as one entry per container. Each part is
// idempotent, so a resumed activity continues where it failed.

// provision creates and configures the container, starts it and installs
// the OS packages k3s needs.
func (e *Engine) provision(ctx context.Context, n plan.Node) error {
	e.logf("%s: creating container", n.Hostname)
	if err := e.createCT(ctx, n); err != nil {
		return err
	}
	if err := e.configureCT(ctx, n); err != nil {
		return err
	}
	e.logf("%s: starting", n.Hostname)
	if err := e.startCT(ctx, n); err != nil {
		return err
	}
	e.logf("%s: installing OS packages", n.Hostname)
	return e.prepareNode(ctx, n)
}

// bootstrapServer installs k3s on main1, waits for the API and reads the
// join tokens.
func (e *Engine) bootstrapServer(ctx context.Context, n plan.Node) error {
	if err := e.installK3s(ctx, n); err != nil {
		return err
	}
	if err := e.waitNode(ctx, n); err != nil {
		return err
	}
	return e.fetchTokens(ctx)
}

// join installs k3s on a server or agent and waits until it is up.
func (e *Engine) join(ctx context.Context, n plan.Node) error {
	if err := e.installK3s(ctx, n); err != nil {
		return err
	}
	return e.waitNode(ctx, n)
}

// finalize waits for all nodes, exports the kubeconfig and records the
// cluster state in the ConfigMap.
func (e *Engine) finalize(ctx context.Context) error {
	if err := e.clusterReady(ctx); err != nil {
		return err
	}
	if err := e.kubeconfig(ctx); err != nil {
		return err
	}
	return e.publishState(ctx)
}
