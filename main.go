package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os/signal"
	"runtime"

	"time"

	"io"
	"os"

	"syscall"

	"github.com/go-kit/kit/endpoint"
	"github.com/go-kit/kit/log"
	httptransport "github.com/go-kit/kit/transport/http"
	"github.com/gorilla/mux"
	"github.com/natefinch/lumberjack"
	"github.com/goofansu/wego/dict"
)

type TextService interface {
	Validate(text string) bool
	Filter(text string) string
}

type textService struct{}

func (textService) Validate(text string) bool {
	return dict.ExistInvalidWord(text) == false
}

func (textService) Filter(text string) string {
	return dict.ReplaceInvalidWords(text)
}

type validateRequest struct {
	S string `json:"message"`
}

type validateResponse struct {
	V bool `json:"result"`
}

type filterRequest struct {
	S string `json:"message"`
}

type filterResponse struct {
	V string `json:"result"`
}

func makeValidateEndpoint(svc TextService) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		req := request.(validateRequest)
		v := svc.Validate(req.S)
		return validateResponse{v}, nil
	}
}

func makeFilterEndpoint(svc TextService) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		req := request.(filterRequest)
		v := svc.Filter(req.S)
		return filterResponse{v}, nil
	}
}

func encodeResponse(_ context.Context, w http.ResponseWriter, response interface{}) error {
	return json.NewEncoder(w).Encode(response)
}

// Not using
func loggingMiddleware(logger log.Logger) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, request interface{}) (interface{}, error) {
			logger.Log("msg", "calling endpoint")
			defer logger.Log("msg", "called endpoint", "took", time.Since(time.Now()))
			return next(ctx, request)
		}
	}
}

type loggingTextServiceMiddleware struct {
	logger log.Logger
	next   TextService
}

func (mw loggingTextServiceMiddleware) Validate(text string) bool {
	defer func(begin time.Time) {
		mw.logger.Log(
			"method", "validate",
			"text", text,
			"took", time.Since(begin),
		)
	}(time.Now())
	return mw.next.Validate(text)
}

func (mw loggingTextServiceMiddleware) Filter(text string) (filtered string) {
	defer func(begin time.Time) {
		mw.logger.Log(
			"method", "filter",
			"text", text,
			"filtered", filtered,
			"took", time.Since(begin),
		)
	}(time.Now())

	filtered = mw.next.Filter(text)
	return
}

func main() {
	var (
		httpAddr = flag.String("http.addr", ":8000", "Address for HTTP server")
		dictPath = flag.String("dict.path", "*.txt", "Files to load as dictionary, glob pattern is supported")
		logDir   = flag.String("log.dir", "", "Log directory")
	)
	flag.Parse()

	runtime.GOMAXPROCS(runtime.NumCPU())
	dict.Load(*dictPath)

	var w io.Writer
	if len(*logDir) > 0 {
		w = &lumberjack.Logger{Dir: *logDir, LocalTime: true}
	} else {
		w = os.Stderr
	}

	var logger log.Logger
	logger = log.NewLogfmtLogger(w)

	var svc TextService
	svc = textService{}
	svc = loggingTextServiceMiddleware{logger, svc}

	var validate endpoint.Endpoint
	validate = makeValidateEndpoint(svc)
	validateHandler := httptransport.NewServer(
		validate,
		func(_ context.Context, r *http.Request) (interface{}, error) {
			message := r.FormValue("message")
			return validateRequest{message}, nil
		},
		encodeResponse,
		httptransport.ServerAfter(),
	)

	var filter endpoint.Endpoint
	filter = makeFilterEndpoint(svc)
	filterHandler := httptransport.NewServer(
		filter,
		func(_ context.Context, r *http.Request) (interface{}, error) {
			message := r.FormValue("message")
			return filterRequest{message}, nil
		},
		encodeResponse,
	)

	r := mux.NewRouter()
	r.Handle("/validate", validateHandler).Methods("POST")
	r.Handle("/filter", filterHandler).Methods("POST")

	// Interrupt handler.
	errc := make(chan error)
	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		errc <- fmt.Errorf("%s", <-c)
	}()

	// HTTP transport.
	go func() {
		logger.Log("transport", "HTTP", "addr", *httpAddr)
		errc <- http.ListenAndServe(*httpAddr, r)
	}()

	logger.Log("msg", "exit", "err", <-errc)
}
