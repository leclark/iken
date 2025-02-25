package httplog

import (
	"bytes"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/bir/iken/httputil"
	"github.com/bir/iken/logctx"
)

// Reference: https://docs.datadoghq.com/logs/log_configuration/attributes_naming_convention/#http-requests
const (
	Duration            = "duration" // In nanoseconds
	HTTPStatusCode      = "http.status_code"
	HTTPMethod          = "http.method"
	HTTPURLDetailsPath  = "http.url_details.path"
	NetworkBytesWritten = "network.bytes_written"
	Operation           = "op"
	Request             = "request.body"
	RequestID           = "http.request_id"
	RequestHeaders      = "request.headers"
	RequestSize         = "network.bytes_read"
	RequestError        = "request.body_error"
	Response            = "response.body"
	TraceID             = "trace_id"
	UserID              = "usr.id"
	Stack               = "error.stack"
)

// MaxRequestBodyLog controls the maximum request body that can be logged.  Anything greater will be truncated.
var MaxRequestBodyLog = 24 * 1024

// now is a utility used for automated testing (overriding the runtime clock).
var now = time.Now

// stackSkip defines the lines to skip in the stack logger - this is determined by the structure of this code.
const stackSkip = 3

// FnShouldLog given a request, return flags that control logging.
// logRequest will disable the entire request logging middleware, default is true.
// logRequestBody will log the body of the request, default is false.
// logResponseBody will log the body of the response, default is false.  This should be disabled for large or streaming
// results.
type FnShouldLog func(r *http.Request) (logRequest, logRequestBody, logResponseBody bool)

func LogRequestBody(_ *http.Request) (bool, bool, bool) { return true, true, false }

func LogAll(_ *http.Request) (bool, bool, bool) { return true, true, true }

// RequestLogger returns a handler that call initializes Op in the context, and logs each request.
func RequestLogger(shouldLog FnShouldLog) func(http.Handler) http.Handler { //nolint: funlen
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := now()

			var logRequest, logRequestBody, logResponse bool
			logRequest = true

			if shouldLog != nil {
				logRequest, logRequestBody, logResponse = shouldLog(r)
			}

			requestID := r.Header.Get(httputil.RequestIDHeader)
			ctx := logctx.SetID(r.Context(), requestID)

			if !logRequest {
				if next != nil {
					next.ServeHTTP(w, r.WithContext(ctx))
				}

				return
			}

			var responseBuffer *bytes.Buffer
			wrappedWriter := httputil.WrapWriter(w)

			if logResponse {
				responseBuffer = bytes.NewBuffer(nil)
				wrappedWriter.Tee(responseBuffer)
			}

			zerolog.Ctx(ctx).UpdateContext(func(logContext zerolog.Context) zerolog.Context {
				if logRequestBody {
					logContext = logBody(logContext, r)
				}

				if requestID != "" {
					logContext = logContext.Str(RequestID, requestID)
				}

				return logContext.
					Str(HTTPMethod, r.Method).
					Str(HTTPURLDetailsPath, r.URL.Path).
					Interface(RequestHeaders, httputil.DumpHeader(r))
			})

			if next != nil {
				next.ServeHTTP(wrappedWriter, r.WithContext(ctx))
			}

			status := wrappedWriter.Status()

			l := zerolog.Ctx(r.Context()).With().
				Int(HTTPStatusCode, status).
				Int(NetworkBytesWritten, wrappedWriter.BytesWritten()).
				Dur(Duration, now().Sub(start))

			if logResponse {
				l = l.Bytes(Response, responseBuffer.Bytes())
			}

			logger := l.Logger()
			var event *zerolog.Event

			switch {
			case status >= http.StatusInternalServerError:
				event = logger.Error()
			case status >= http.StatusBadRequest:
				event = logger.Warn()
			default:
				event = logger.Info()
			}

			event.Msgf("%d %s %s", status, r.Method, r.URL)
		})
	}
}

func logBody(logContext zerolog.Context, r *http.Request) zerolog.Context {
	body, err := httputil.DumpBody(r)
	if err != nil {
		logContext = logContext.Str(RequestError, err.Error())
	} else {
		size := len(body)
		logContext = logContext.Int(RequestSize, size)

		if size > MaxRequestBodyLog {
			logContext = logContext.Bytes(Request, body[:MaxRequestBodyLog])
		} else {
			logContext = logContext.Bytes(Request, body)
		}
	}

	return logContext
}
