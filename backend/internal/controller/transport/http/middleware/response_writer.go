package middleware

import "net/http"

type ResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func NewResponseWriter(w http.ResponseWriter) *ResponseWriter {
	return &ResponseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (w *ResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *ResponseWriter) Write(body []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

func (w *ResponseWriter) Status() int {
	return w.status
}

func (w *ResponseWriter) Bytes() int {
	return w.bytes
}

func (w *ResponseWriter) Written() bool {
	return w.wrote
}
