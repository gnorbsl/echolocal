//go:build !board_biscuit

package gui

import (
	"github.com/ygelfand/libcountertop/pkg/display/screens"
	"github.com/ygelfand/libcountertop/pkg/display/widgets"

	"github.com/ygelfand/echolocal/internal/feature/remotescreen"
)

func init() {
	extensions = append(extensions, func(look *widgets.UI, o *screens.Options) {
		r := remotescreen.Get()
		r.Use(look)
		o.Views = append(o.Views, r.Screen)
	})
}
