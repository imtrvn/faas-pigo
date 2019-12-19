package function

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"time"

	pigo "github.com/esimov/pigo/core"
	"github.com/fogleman/gg"
)

var dc *gg.Context

// FaceDetector struct contains Pigo face detector general settings.
type FaceDetector struct {
	cascadeFile  string
	minSize      int
	maxSize      int
	shiftFactor  float64
	scaleFactor  float64
	iouThreshold float64
}

// DetectionResult contains the coordinates of the detected faces and the base64 converted image.
type DetectionResult struct {
	Faces       []image.Rectangle
	ImageBase64 string
	ImageName   string
}

func verifyRequest(r *http.Request) error {
	if _, ok := r.MultipartForm.File["image"]; !ok {
		return fmt.Errorf("No image uploaded. Please try again")
	}
	return nil
}

//Handle function
func Handle(w http.ResponseWriter, r *http.Request) {
	parseErr := r.ParseMultipartForm(32 << 20)
	if parseErr != nil {
		http.Error(w, "failed to parse multipart message", http.StatusBadRequest)
		return
	}

	if r.MultipartForm == nil || r.MultipartForm.File == nil {
		http.Error(w, "expecting multipart form file", http.StatusBadRequest)
		return
	}
	if err := verifyRequest(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var (
		resp  DetectionResult
		rects []image.Rectangle
		image []byte
	)
	var outcome []DetectionResult
	for _, h := range r.MultipartForm.File["image"] {
		file, err := h.Open()
		if err != nil {
			http.Error(w, "failed to get media form file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		tmpfile, err := ioutil.TempFile("/tmp", "image")
		if err != nil {
			http.Error(w, "Unable to create temp file", http.StatusInternalServerError)
			return
		}
		if _, err = io.Copy(tmpfile, file); err != nil {
			http.Error(w, "Unable to copy the source URI to the destionation file", http.StatusInternalServerError)
			return
		}
		defer os.Remove(tmpfile.Name())

		fd := NewFaceDetector("./data/facefinder", 20, 2000, 0.1, 1.1, 0.18)
		faces, err := fd.DetectFaces(tmpfile.Name())

		if err != nil {
			http.Error(w, "Error on face detection", http.StatusInternalServerError)
			return
		}
		var errs error
		rects, image, errs = fd.DrawFaces(faces, false)
		if errs != nil {
			http.Error(w, "Error creating image output", http.StatusInternalServerError)
			return
		}
		resp = DetectionResult{
			Faces:       rects,
			ImageBase64: base64.StdEncoding.EncodeToString(image),
			ImageName:   h.Filename,
		}
		outcome = append(outcome, resp)
	}
	j, err := json.Marshal(outcome)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error encoding output: %s", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(j)
}

// NewFaceDetector initialises the constructor function.
func NewFaceDetector(cf string, minSize, maxSize int, shf, scf, iou float64) *FaceDetector {
	return &FaceDetector{
		cascadeFile:  cf,
		minSize:      minSize,
		maxSize:      maxSize,
		shiftFactor:  shf,
		scaleFactor:  scf,
		iouThreshold: iou,
	}
}

// DetectFaces run the detection algorithm over the provided source image.
func (fd *FaceDetector) DetectFaces(source string) ([]pigo.Detection, error) {
	src, err := pigo.GetImage(source)
	if err != nil {
		return nil, err
	}

	pixels := pigo.RgbToGrayscale(src)
	cols, rows := src.Bounds().Max.X, src.Bounds().Max.Y

	dc = gg.NewContext(cols, rows)
	dc.DrawImage(src, 0, 0)

	cParams := pigo.CascadeParams{
		MinSize:     fd.minSize,
		MaxSize:     fd.maxSize,
		ShiftFactor: fd.shiftFactor,
		ScaleFactor: fd.scaleFactor,
		ImageParams: pigo.ImageParams{
			Pixels: pixels,
			Rows:   rows,
			Cols:   cols,
			Dim:    cols,
		},
	}

	cascadeFile, err := ioutil.ReadFile(fd.cascadeFile)
	if err != nil {
		return nil, err
	}

	pigo := pigo.NewPigo()
	// Unpack the binary file. This will return the number of cascade trees,
	// the tree depth, the threshold and the prediction from tree's leaf nodes.
	classifier, err := pigo.Unpack(cascadeFile)
	if err != nil {
		return nil, err
	}

	// Run the classifier over the obtained leaf nodes and return the detection results.
	// The result contains quadruplets representing the row, column, scale and detection score.
	faces := classifier.RunCascade(cParams, 0)

	// Calculate the intersection over union (IoU) of two clusters.
	faces = classifier.ClusterDetections(faces, fd.iouThreshold)

	return faces, nil
}

// DrawFaces marks the detected faces with a circle in case isCircle is true, otherwise marks with a rectangle.
func (fd *FaceDetector) DrawFaces(faces []pigo.Detection, isCircle bool) ([]image.Rectangle, []byte, error) {
	var (
		qThresh float32 = 5.0
		rects   []image.Rectangle
	)

	for _, face := range faces {
		if face.Q > qThresh {
			if isCircle {
				dc.DrawArc(
					float64(face.Col),
					float64(face.Row),
					float64(face.Scale/2),
					0,
					2*math.Pi,
				)
			} else {
				dc.DrawRectangle(
					float64(face.Col-face.Scale/2),
					float64(face.Row-face.Scale/2),
					float64(face.Scale),
					float64(face.Scale),
				)
			}
			rects = append(rects, image.Rect(
				face.Col-face.Scale/2,
				face.Row-face.Scale/2,
				face.Scale,
				face.Scale,
			))
			dc.SetLineWidth(2.0)
			dc.SetStrokeStyle(gg.NewSolidPattern(color.RGBA{R: 255, G: 255, B: 0, A: 255}))
			dc.Stroke()
		}
	}

	img := dc.Image()

	filename := fmt.Sprintf("/tmp/%d.jpg", time.Now().UnixNano())

	output, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0755)
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(filename)

	jpeg.Encode(output, img, &jpeg.Options{Quality: 100})

	rf, err := ioutil.ReadFile(filename)
	return rects, rf, err
}