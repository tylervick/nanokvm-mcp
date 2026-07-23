package backend

import (
	"context"
	"errors"

	"github.com/scgreenhalgh/nanokvm-mcp/internal/nanokvm"
)

type Public struct{ kvm *nanokvm.Client }

func NewPublic(kvm *nanokvm.Client) *Public { return &Public{kvm: kvm} }
func (p *Public) Name() string              { return "public" }
func (p *Public) Screenshot(context.Context, ScreenshotOpts) (Shot, error) {
	return Shot{}, errors.New("public backend screenshot not implemented")
}
func (p *Public) Input(context.Context, []Action) error {
	return errors.New("public backend input not implemented")
}
