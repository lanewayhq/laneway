package capage

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// quietZone is the light margin a QR code needs around it, in modules.
const quietZone = 4

// qrSVG draws text as a QR code: dark modules on a light square, with the
// quiet zone, in a viewBox of whole modules. The colours are fixed, as a
// scanner needs dark on light whatever the page's theme.
func qrSVG(text string) ([]byte, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	size := code.Size + 2*quietZone
	var path strings.Builder
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; {
			if !code.Black(x, y) {
				x++
				continue
			}
			run := 1
			for x+run < code.Size && code.Black(x+run, y) {
				run++
			}
			fmt.Fprintf(&path, "M%d %dh%dv1h-%dz", x+quietZone, y+quietZone, run, run)
			x += run
		}
	}
	return fmt.Appendf(nil, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`+
		`<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="%s"/></svg>`+"\n",
		size, size, size, size, path.String()), nil
}
