package main

// Небольшие собственные виджеты: карточка, аватар с инициалом и точка
// статуса. Цвета берутся из темы на каждом Refresh, поэтому светлая и тёмная
// версии переключаются вместе с системной темой.

import (
	"image/color"
	"strings"
	"unicode"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// --- карточка ---

type card struct {
	widget.BaseWidget
	content fyne.CanvasObject
	pad     float32
}

func newCard(content fyne.CanvasObject) *card {
	c := &card{content: content, pad: 8}
	c.ExtendBaseWidget(c)
	return c
}

func (c *card) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(color.Transparent)
	bg.StrokeWidth = 1
	r := &cardRenderer{c: c, bg: bg}
	r.Refresh()
	return r
}

type cardRenderer struct {
	c  *card
	bg *canvas.Rectangle
}

func (r *cardRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.c.content.Move(fyne.NewPos(r.c.pad, r.c.pad))
	r.c.content.Resize(fyne.NewSize(size.Width-2*r.c.pad, size.Height-2*r.c.pad))
}

func (r *cardRenderer) MinSize() fyne.Size {
	m := r.c.content.MinSize()
	return fyne.NewSize(m.Width+2*r.c.pad, m.Height+2*r.c.pad)
}

func (r *cardRenderer) Refresh() {
	r.bg.FillColor = theme.ColorForWidget(colorCard, r.c)
	r.bg.StrokeColor = theme.ColorForWidget(colorBorder, r.c)
	r.bg.CornerRadius = theme.SizeForWidget(theme.SizeNameCardRadius, r.c)
	r.bg.Refresh()
	r.c.content.Refresh()
}

func (r *cardRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.c.content}
}
func (r *cardRenderer) Destroy() {}

// --- аватар ---

type avatar struct {
	widget.BaseWidget
	letter string
	size   float32
}

func newAvatar(name string, size float32) *avatar {
	a := &avatar{letter: initial(name), size: size}
	a.ExtendBaseWidget(a)
	return a
}

func initial(name string) string {
	for _, r := range strings.TrimSpace(name) {
		return string(unicode.ToUpper(r))
	}
	return "?"
}

func (a *avatar) CreateRenderer() fyne.WidgetRenderer {
	circle := canvas.NewCircle(color.Transparent)
	txt := canvas.NewText(a.letter, color.Black)
	txt.TextStyle = fyne.TextStyle{Bold: true}
	txt.TextSize = a.size * 0.45
	txt.Alignment = fyne.TextAlignCenter
	r := &avatarRenderer{a: a, circle: circle, txt: txt}
	r.Refresh()
	return r
}

type avatarRenderer struct {
	a      *avatar
	circle *canvas.Circle
	txt    *canvas.Text
}

func (r *avatarRenderer) Layout(size fyne.Size) {
	r.circle.Resize(size)
	ts := r.txt.MinSize()
	r.txt.Resize(ts)
	r.txt.Move(fyne.NewPos((size.Width-ts.Width)/2, (size.Height-ts.Height)/2))
}
func (r *avatarRenderer) MinSize() fyne.Size { return fyne.NewSquareSize(r.a.size) }
func (r *avatarRenderer) Refresh() {
	r.circle.FillColor = theme.ColorForWidget(colorSoft, r.a)
	r.txt.Color = theme.ColorForWidget(theme.ColorNamePrimary, r.a)
	r.txt.Text = r.a.letter
	r.circle.Refresh()
	r.txt.Refresh()
}
func (r *avatarRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.circle, r.txt}
}
func (r *avatarRenderer) Destroy() {}

// --- точка статуса ---

type dot struct {
	widget.BaseWidget
	name fyne.ThemeColorName
}

func newDot(name fyne.ThemeColorName) *dot {
	d := &dot{name: name}
	d.ExtendBaseWidget(d)
	return d
}

func (d *dot) SetColor(name fyne.ThemeColorName) {
	if d.name != name {
		d.name = name
		d.Refresh()
	}
}

func (d *dot) CreateRenderer() fyne.WidgetRenderer {
	c := canvas.NewCircle(color.Transparent)
	r := &dotRenderer{d: d, c: c}
	r.Refresh()
	return r
}

type dotRenderer struct {
	d *dot
	c *canvas.Circle
}

func (r *dotRenderer) Layout(size fyne.Size) { r.c.Resize(size) }
func (r *dotRenderer) MinSize() fyne.Size    { return fyne.NewSquareSize(9) }
func (r *dotRenderer) Refresh() {
	r.c.FillColor = theme.ColorForWidget(r.d.name, r.d)
	r.c.Refresh()
}
func (r *dotRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.c} }
func (r *dotRenderer) Destroy()                     {}

// vcenter выравнивает объект по вертикали в строке (HBox/Border).
func vcenter(o fyne.CanvasObject) fyne.CanvasObject {
	return fyne.NewContainerWithLayout(centerLayout{}, o)
}

type centerLayout struct{}

func (centerLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		m := o.MinSize()
		o.Resize(fyne.NewSize(size.Width, m.Height))
		o.Move(fyne.NewPos(0, (size.Height-m.Height)/2))
	}
}

func (centerLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var s fyne.Size
	for _, o := range objs {
		m := o.MinSize()
		if m.Width > s.Width {
			s.Width = m.Width
		}
		if m.Height > s.Height {
			s.Height = m.Height
		}
	}
	return s
}
