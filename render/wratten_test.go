package render

// The rule for this catalogue: a curve may only be entered if it was read from
// the compilation and checks out against that filter's printed luminous
// transmittance. This test is the check. It recomputes each curve's luminous
// transmittance with CIE illuminant C and the CIE 1931 2° observer, and fails
// a curve that disagrees with the printed value by more than 0.15 percentage
// points (the transcription reproduces all nine within 0.06).

import (
	"encoding/json"
	"testing"
)

// 400..700 nm at 10 nm.
var (
	cieC = [...]float64{
		63.30, 80.60, 98.10, 112.40, 121.50, 124.00, 123.10, 123.80, 123.90, 120.70,
		112.10, 102.30, 96.90, 98.00, 102.10, 105.20, 105.30, 102.30, 97.80, 93.20,
		89.70, 88.40, 88.10, 88.00, 87.80, 88.20, 87.90, 86.30, 84.00, 80.20, 76.30,
	}
	cieYBar = [...]float64{
		0.000396, 0.001210, 0.004000, 0.011600, 0.023000, 0.038000, 0.060000, 0.090980, 0.139020, 0.208020,
		0.323000, 0.503000, 0.710000, 0.862000, 0.954000, 0.994950, 0.995000, 0.952000, 0.870000, 0.757000,
		0.631000, 0.503000, 0.381000, 0.265000, 0.175000, 0.107000, 0.061000, 0.032000, 0.017000, 0.008210, 0.004102,
	}
)

// luminous computes the luminous transmittance (percent) of a curve given as
// percent transmittance at 400..700 nm in 10 nm steps.
func luminous(curve []*float64) float64 {
	var num, den float64
	for i := range cieC {
		den += cieC[i] * cieYBar[i]
		if curve[i] != nil {
			num += (*curve[i] / 100) * cieC[i] * cieYBar[i]
		}
	}
	return 100 * num / den
}

func TestWrattenCurvesMatchPrintedLuminous(t *testing.T) {
	var cat struct {
		Filters map[string]struct {
			Wratten  string     `json:"wratten"`
			Luminous float64    `json:"luminousTransmittance"`
			Percent  []*float64 `json:"transmittancePercent"`
		} `json:"filters"`
	}
	if err := json.Unmarshal(wrattenJSON, &cat); err != nil {
		t.Fatalf("wratten.json: %v", err)
	}
	if len(cat.Filters) != 9 {
		t.Errorf("%d curves entered, want 9 (Wratten 47 is deliberately not entered)", len(cat.Filters))
	}
	for name, f := range cat.Filters {
		if len(f.Percent) != 31 {
			t.Errorf("%s: %d samples, want 31 (400-700 nm at 10 nm)", name, len(f.Percent))
			continue
		}
		got := luminous(f.Percent)
		if d := got - f.Luminous; d > 0.15 || d < -0.15 {
			t.Errorf("%s (Wratten %s): luminous transmittance %.2f%%, printed %.1f%% — the curve does not match its printed value",
				name, f.Wratten, got, f.Luminous)
		}
	}
}

func TestWrattenTransmissionInterpolates(t *testing.T) {
	f := WrattenTransmission("red") // Wratten 25: 12.6% at 590 nm, 50.0% at 600 nm
	if f == nil {
		t.Fatal("no curve for red")
	}
	if got, want := f(590), 0.126; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("T(590) = %v, want %v", got, want)
	}
	if got := f(595); got < 0.126 || got > 0.50 {
		t.Errorf("T(595) = %v, outside its neighbours", got)
	}
	// Outside the table the curve is held flat.
	if got, want := f(380), f(400); got != want {
		t.Errorf("T(380) = %v, want the 400 nm value %v", got, want)
	}
	if got, want := f(720), f(700); got != want {
		t.Errorf("T(720) = %v, want the 700 nm value %v", got, want)
	}
	// Wratten 47 is deliberately absent.
	if WrattenTransmission("blue") != nil {
		t.Error("blue (Wratten 47) has a curve; the compilation disagrees about it, so it must stay approximate")
	}
}
