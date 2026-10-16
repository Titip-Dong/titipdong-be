// Package monitoring owns application-level Prometheus instrumentation.
package monitoring

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics contains the bounded-label metric collectors exposed by the API.
// Values supplied to the business methods must be controlled enum values, never
// identifiers, emails, product names, payment IDs, or any other user data.
type Metrics struct {
	registry          *prometheus.Registry
	requests          *prometheus.CounterVec
	requestDuration   *prometheus.HistogramVec
	inFlight          *prometheus.GaugeVec
	dbQueries         *prometheus.CounterVec
	dbQueryDuration   *prometheus.HistogramVec
	orderTransitions  *prometheus.CounterVec
	paymentCallbacks  *prometheus.CounterVec
	backgroundJobs    *prometheus.CounterVec
	backgroundJobTime *prometheus.HistogramVec
}

// New creates an isolated registry. An isolated registry keeps tests and
// future multiple-app processes free from collisions with the global registry.
func New() *Metrics {
	m := &Metrics{registry: prometheus.NewRegistry()}
	m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_server_requests_total",
		Help: "Total HTTP requests completed by the API.",
	}, []string{"method", "route", "status_code"})
	m.requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_server_request_duration_seconds",
		Help:    "HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route", "status_code"})
	m.inFlight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "http_server_in_flight_requests",
		Help: "HTTP requests currently being processed by the API.",
	}, []string{"method", "route"})
	m.dbQueries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "db_queries_total",
		Help: "Total database queries completed by operation and outcome.",
	}, []string{"operation", "outcome"})
	m.dbQueryDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "db_query_duration_seconds",
		Help:    "Database query duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"operation", "outcome"})
	m.orderTransitions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "orders_state_transitions_total",
		Help: "Total order state transitions completed by the Order module.",
	}, []string{"from_status", "to_status"})
	m.paymentCallbacks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mock_payment_callbacks_total",
		Help: "Total mock payment callbacks by bounded outcome.",
	}, []string{"outcome"})
	m.backgroundJobs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "background_jobs_total",
		Help: "Total background jobs completed by type and outcome.",
	}, []string{"job_type", "outcome"})
	m.backgroundJobTime = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "background_job_duration_seconds",
		Help:    "Background job duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"job_type", "outcome"})

	m.registry.MustRegister(
		m.requests,
		m.requestDuration,
		m.inFlight,
		m.dbQueries,
		m.dbQueryDuration,
		m.orderTransitions,
		m.paymentCallbacks,
		m.backgroundJobs,
		m.backgroundJobTime,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler exposes metrics in the Prometheus text exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// FiberMiddleware records normalized route-level API traffic metrics.
func (m *Metrics) FiberMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		method := c.Method()
		route := routeTemplate(c)
		m.inFlight.WithLabelValues(method, route).Inc()
		started := time.Now()

		err := c.Next()
		m.inFlight.WithLabelValues(method, route).Dec()

		status := c.Response().StatusCode()
		if err != nil {
			if fiberError, ok := err.(*fiber.Error); ok {
				status = fiberError.Code
			} else {
				status = fiber.StatusInternalServerError
			}
		}
		// Calling Route after the handler completes gives the normalized template
		// (for example /orders/:id), not a user-controlled path with an ID.
		route = routeTemplate(c)
		statusCode := strconv.Itoa(status)
		m.requests.WithLabelValues(method, route, statusCode).Inc()
		m.requestDuration.WithLabelValues(method, route, statusCode).Observe(time.Since(started).Seconds())

		return err
	}
}

// ObserveDatabaseQuery records a completed database operation. Operation and
// outcome must be fixed vocabulary values owned by the calling module.
func (m *Metrics) ObserveDatabaseQuery(operation, outcome string, duration time.Duration) {
	m.dbQueries.WithLabelValues(operation, outcome).Inc()
	m.dbQueryDuration.WithLabelValues(operation, outcome).Observe(duration.Seconds())
}

// RecordOrderTransition records a valid state transition after it commits.
func (m *Metrics) RecordOrderTransition(fromStatus, toStatus string) {
	m.orderTransitions.WithLabelValues(fromStatus, toStatus).Inc()
}

// RecordMockPaymentCallback records a bounded callback result such as success,
// duplicate, or rejected.
func (m *Metrics) RecordMockPaymentCallback(outcome string) {
	m.paymentCallbacks.WithLabelValues(outcome).Inc()
}

// ObserveBackgroundJob records a completed job. Job type and outcome must be
// fixed vocabulary values, for example notification/success.
func (m *Metrics) ObserveBackgroundJob(jobType, outcome string, duration time.Duration) {
	m.backgroundJobs.WithLabelValues(jobType, outcome).Inc()
	m.backgroundJobTime.WithLabelValues(jobType, outcome).Observe(duration.Seconds())
}

func routeTemplate(c *fiber.Ctx) string {
	route := c.Route()
	if route == nil || route.Path == "" || route.Path == "*" {
		return "unmatched"
	}
	return route.Path
}
