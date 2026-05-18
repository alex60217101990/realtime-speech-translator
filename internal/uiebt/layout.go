package uiebt

import "image"

// Regions is the static layout split for the Main tab — the
// reference mock divides the window into six rectangles. We
// recompute on every Draw because Layout may have resized the
// logical viewport.
//
//	┌──────────────────── Window ─────────────────────┐
//	│              Header                             │
//	├────────────┬─────────────────┬──────────────────┤
//	│ LeftPane   │                 │   RightPane      │
//	│ (RU cards) │     Sphere      │   (EN cards)     │
//	│            │                 │                  │
//	├────────────┴─────────────────┴──────────────────┤
//	│              Bottom controls                    │
//	├──────────────────────────────────────────────── │
//	│              Status strip                       │
//	└─────────────────────────────────────────────────┘
type Regions struct {
	Window  image.Rectangle
	Header  image.Rectangle
	Left    image.Rectangle
	Center  image.Rectangle
	Right   image.Rectangle
	Bottom  image.Rectangle
	Status  image.Rectangle
}

// LayoutFor splits a viewport into the six fixed regions. Sizes
// chosen so the mock's 1280x800 dimensions look right; on a larger
// window the center pane (sphere) absorbs the extra horizontal
// space — the side card stacks keep their published width.
func LayoutFor(viewW, viewH int) Regions {
	r := Regions{Window: image.Rect(0, 0, viewW, viewH)}

	// Status strip at the very bottom — thin, full width.
	statusH := 36
	r.Status = image.Rect(0, viewH-statusH, viewW, viewH)

	// Bottom controls — fixed height, full width above the status
	// strip.
	bottomH := 120
	r.Bottom = image.Rect(0, viewH-statusH-bottomH, viewW, viewH-statusH)

	// Header — fixed height across the top.
	headerH := 56
	r.Header = image.Rect(0, 0, viewW, headerH)

	// Side panes have a fixed width close to the mock.
	sideW := 360
	bodyTop := headerH + SpaceL
	bodyBot := r.Bottom.Min.Y - SpaceL

	r.Left = image.Rect(SpaceL, bodyTop, SpaceL+sideW, bodyBot)
	r.Right = image.Rect(viewW-SpaceL-sideW, bodyTop, viewW-SpaceL, bodyBot)
	r.Center = image.Rect(r.Left.Max.X+SpaceL, bodyTop, r.Right.Min.X-SpaceL, bodyBot)

	return r
}

// inset shrinks a rectangle uniformly.
func inset(r image.Rectangle, dx, dy int) image.Rectangle {
	return image.Rect(r.Min.X+dx, r.Min.Y+dy, r.Max.X-dx, r.Max.Y-dy)
}
