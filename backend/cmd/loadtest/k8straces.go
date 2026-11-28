package main

import (
	"fmt"
	"math/rand"
	"time"

	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// The k8s trace profile: requests to the k8s logs profile's shop. They enter
// at the web tier and fan out over its APIs, each ending in Postgres, Redis,
// Kafka or an outside HTTP call; Kafka consumers start traces of their own.
// A trace has 4 to about 40 spans over 2 to 5 services, on the same churning
// pods as the logs. About 2% fail at a leaf, most failures surfacing as 5xx
// up the tree with an exception event where they started, and about 2% of
// leaf calls are slow (0.5 to 3 s). The simulated clock counts spans: each
// trace starts at its root span's place on it.

// traceFlow builds one kind of request from its entry span's start.
type traceFlow struct {
	weight int
	build  func(b *traceBuilder, at time.Time)
}

// traceBuilder collects one batch's spans, grouped by the pod that sent them.
type traceBuilder struct {
	rng      *rand.Rand
	services map[string]*k8sService
	traceID  []byte
	at       time.Time
	// current: each service's pod now; pods: every pod a span names.
	current map[string]k8sPod
	pods    map[string]k8sPod
	spans   []*tracev1.Span
	podOf   []string
	// failed: a leaf failed in the current trace; spans above it may fail too.
	failed bool
}

var k8sTraceFlows = []traceFlow{
	{45, productPage},
	{20, searchPage},
	{15, addToCart},
	{10, checkout},
	{7, orderEvent},
	{3, adminReport},
}

func newK8sTracesSender(otlp *otlpSender, start time.Time, spansPerSecond float64, pods int) *k8sLogsSender {
	clock := &simClock{start: start, perLine: float64(time.Second) / spansPerSecond}
	services := map[string]*k8sService{}
	for _, svc := range k8sServices(pods) {
		services[svc.name] = svc
	}
	return &k8sLogsSender{
		otlp:    otlp,
		clock:   clock,
		profile: "k8s",
		build: func(rng *rand.Rand, first int64, n int) proto.Message {
			return buildK8sTraceRequest(rng, services, clock, first, n)
		},
		backoff: 250 * time.Millisecond,
	}
}

// buildK8sTraceRequest is exactly n spans: whole traces, the last one cut
// short. Parents come before their children, so a cut trace is still a tree.
func buildK8sTraceRequest(
	rng *rand.Rand, services map[string]*k8sService, clock *simClock, first int64, n int,
) *collectortracev1.ExportTraceServiceRequest {
	b := &traceBuilder{rng: rng, services: services, current: map[string]k8sPod{}, pods: map[string]k8sPod{}}
	for len(b.spans) < n {
		b.traceID = randomBytes(rng, 16)
		b.at = clock.at(first + int64(len(b.spans)))
		b.failed = false
		pickFlow(rng).build(b, b.at)
	}
	b.spans, b.podOf = b.spans[:n], b.podOf[:n]

	byPod := map[string][]*tracev1.Span{}
	var order []string
	for i, span := range b.spans {
		if _, ok := byPod[b.podOf[i]]; !ok {
			order = append(order, b.podOf[i])
		}
		byPod[b.podOf[i]] = append(byPod[b.podOf[i]], span)
	}
	request := &collectortracev1.ExportTraceServiceRequest{}
	for _, name := range order {
		request.ResourceSpans = append(request.ResourceSpans, &tracev1.ResourceSpans{
			Resource:   k8sResource(b.pods[name]),
			ScopeSpans: []*tracev1.ScopeSpans{{Spans: byPod[name]}},
		})
	}
	return request
}

func productPage(b *traceBuilder, at time.Time) {
	slug := productSlug(b.rng)
	b.server(
		nil,
		"storefront-web",
		"GET /products/{slug}",
		"/products/"+slug,
		at,
		func(entry *tracev1.Span, t time.Time) time.Time {
			id := fmt.Sprint(1000 + b.rng.Intn(90000))
			t = b.call(
				entry,
				"storefront-web",
				"catalog-api",
				"GET",
				"/api/v1/products/{id}",
				"/api/v1/products/"+id,
				t,
				func(s *tracev1.Span, t time.Time) time.Time {
					t = b.cache(s, "catalog-api", "GET", "product:"+id, t)
					if b.rng.Intn(4) == 0 {
						t = b.db(s, "catalog-api", "SELECT", "SELECT id, name, price FROM products WHERE id = $1", t)
					}
					return t
				},
			)
			t = b.call(
				entry,
				"storefront-web",
				"pricing-api",
				"GET",
				"/api/v1/prices",
				"/api/v1/prices?ids="+id,
				t,
				func(s *tracev1.Span, t time.Time) time.Time { return b.cache(s, "pricing-api", "MGET", "price:"+id, t) },
			)
			t = b.call(
				entry,
				"storefront-web",
				"inventory-api",
				"GET",
				"/api/v1/inventory/{id}",
				"/api/v1/inventory/"+id,
				t,
				func(s *tracev1.Span, t time.Time) time.Time {
					return b.db(
						s,
						"inventory-api",
						"SELECT",
						"SELECT sku, available FROM inventory_levels WHERE product_id = $1",
						t,
					)
				},
			)
			if b.rng.Intn(3) > 0 {
				t = b.call(
					entry,
					"storefront-web",
					"recommendations",
					"GET",
					"/api/v1/recommendations",
					"/api/v1/recommendations?product="+id,
					t,
					func(s *tracev1.Span, t time.Time) time.Time {
						return b.call(
							s,
							"recommendations",
							"search-api",
							"POST",
							"/api/v1/search",
							"/api/v1/search",
							t,
							nil,
						)
					},
				)
			}
			return t
		},
	)
}

func searchPage(b *traceBuilder, at time.Time) {
	b.server(
		nil,
		"storefront-web",
		"GET /search",
		"/search?q="+pick(b.rng, k8sItems),
		at,
		func(entry *tracev1.Span, t time.Time) time.Time {
			t = b.call(entry, "storefront-web", "search-api", "POST", "/api/v1/search", "/api/v1/search", t,
				func(s *tracev1.Span, t time.Time) time.Time {
					return b.external(s, "search-api", "POST", "search.internal.example.com", "/products/_search", t)
				})
			// The results page fetches each hit's card.
			for range 3 + b.rng.Intn(10) {
				id := fmt.Sprint(1000 + b.rng.Intn(90000))
				t = b.call(
					entry,
					"storefront-web",
					"catalog-api",
					"GET",
					"/api/v1/products/{id}",
					"/api/v1/products/"+id,
					t,
					func(s *tracev1.Span, t time.Time) time.Time {
						return b.cache(s, "catalog-api", "GET", "product:"+id, t)
					},
				)
			}
			return t
		},
	)
}

func addToCart(b *traceBuilder, at time.Time) {
	cart := hexToken(b.rng, 12)
	path := "/api/v1/carts/" + cart + "/items"
	b.server(nil, "storefront-web", "POST /cart", "/cart", at, func(entry *tracev1.Span, t time.Time) time.Time {
		t = b.call(entry, "storefront-web", "sessions-api", "GET", "/api/v1/sessions", "/api/v1/sessions", t,
			func(s *tracev1.Span, t time.Time) time.Time {
				return b.cache(s, "sessions-api", "GET", "session:"+hexToken(b.rng, 16), t)
			})
		return b.call(entry, "storefront-web", "carts-api", "POST", "/api/v1/carts/{id}/items", path, t,
			func(s *tracev1.Span, t time.Time) time.Time {
				t = b.cache(s, "carts-api", "HSET", "cart:"+cart, t)
				return b.db(
					s,
					"carts-api",
					"INSERT",
					"INSERT INTO cart_items (cart_id, sku, qty) VALUES ($1, $2, $3)",
					t,
				)
			})
	})
}

func checkout(b *traceBuilder, at time.Time) {
	order := orderID(b.rng)
	b.server(nil, "checkout-web", "POST /checkout", "/checkout", at, func(entry *tracev1.Span, t time.Time) time.Time {
		t = b.call(
			entry,
			"checkout-web",
			"identity-api",
			"GET",
			"/api/v1/customers/{id}",
			"/api/v1/customers/"+userID(b.rng),
			t,
			func(s *tracev1.Span, t time.Time) time.Time {
				return b.db(s, "identity-api", "SELECT", "SELECT id, email FROM customers WHERE id = $1", t)
			},
		)
		t = b.call(
			entry,
			"checkout-web",
			"carts-api",
			"GET",
			"/api/v1/carts/{id}",
			"/api/v1/carts/"+hexToken(b.rng, 12),
			t,
			func(s *tracev1.Span, t time.Time) time.Time {
				return b.cache(s, "carts-api", "HGETALL", "cart:"+hexToken(b.rng, 12), t)
			},
		)
		t = b.call(entry, "checkout-web", "tax-service", "POST", "/api/v1/tax/quote", "/api/v1/tax/quote", t, nil)
		t = b.call(entry, "checkout-web", "orders-api", "POST", "/api/v1/orders", "/api/v1/orders", t,
			func(s *tracev1.Span, t time.Time) time.Time {
				t = b.db(
					s,
					"orders-api",
					"INSERT",
					"INSERT INTO orders (id, customer_id, total) VALUES ($1, $2, $3)",
					t,
				)
				for range 1 + b.rng.Intn(5) {
					t = b.db(
						s,
						"orders-api",
						"INSERT",
						"INSERT INTO order_items (order_id, sku, qty, price) VALUES ($1, $2, $3, $4)",
						t,
					)
				}
				t = b.call(
					s,
					"orders-api",
					"payments-api",
					"POST",
					"/api/v1/payments/{id}/capture",
					"/api/v1/payments/"+order+"/capture",
					t,
					func(p *tracev1.Span, t time.Time) time.Time {
						return b.call(p, "payments-api", "card-gateway", "POST", "/v1/authorize", "/v1/authorize", t,
							func(g *tracev1.Span, t time.Time) time.Time {
								return b.external(
									g,
									"card-gateway",
									"POST",
									"acquirer.example.net",
									"/v2/authorizations",
									t,
								)
							})
					},
				)
				return b.publish(s, "orders-api", "orders.v1", order, t)
			})
		return t
	})
}

func orderEvent(b *traceBuilder, at time.Time) {
	consumer := pick(b.rng, []string{"order-projector", "email-sender", "shipping-worker"})
	order := orderID(b.rng)
	b.consume(consumer, "orders.v1", order, at, func(s *tracev1.Span, t time.Time) time.Time {
		switch consumer {
		case "email-sender":
			t = b.db(s, consumer, "SELECT", "SELECT email FROM customers WHERE id = $1", t)
			return b.external(s, consumer, "POST", "mail.example.com", "/v3/send", t)
		case "shipping-worker":
			t = b.external(s, consumer, "POST", "rates.example.org", "/quotes", t)
			return b.db(s, consumer, "INSERT", "INSERT INTO shipments (order_id, carrier) VALUES ($1, $2)", t)
		}
		for range 2 + b.rng.Intn(6) {
			t = b.db(s, consumer, "UPDATE", "UPDATE order_projection SET status = $2 WHERE order_id = $1", t)
		}
		return t
	})
}

func adminReport(b *traceBuilder, at time.Time) {
	b.server(
		nil,
		"admin-web",
		"GET /admin/reports/{name}",
		"/admin/reports/sales-by-day",
		at,
		func(entry *tracev1.Span, t time.Time) time.Time {
			return b.call(
				entry,
				"admin-web",
				"orders-api",
				"GET",
				"/api/v1/orders",
				"/api/v1/orders?from=2026-11-01&limit=500",
				t,
				func(s *tracev1.Span, t time.Time) time.Time {
					return b.db(
						s,
						"orders-api",
						"SELECT",
						"SELECT date_trunc('day', created_at), sum(total) FROM orders GROUP BY 1",
						t,
					)
				},
			)
		},
	)
}

// server is an entry span of service (a SERVER span, under parent when it
// is the far end of a call) running body from just after its start.
func (b *traceBuilder) server(
	parent *tracev1.Span, service, name, path string, at time.Time, body func(*tracev1.Span, time.Time) time.Time,
) time.Time {
	span := b.span(parent, service, name, tracev1.Span_SPAN_KIND_SERVER, at)
	method, route := splitName(name)
	span.Attributes = append(span.Attributes,
		strAttr("http.request.method", method), strAttr("http.route", route), strAttr("url.path", path))
	if parent == nil {
		span.Attributes = append(span.Attributes, strAttr("user.id", userID(b.rng)),
			strAttr("user_agent.original", pick(b.rng, k8sUserAgents)))
	}
	end := at.Add(b.self(1, 4))
	if body != nil {
		end = body(span, end).Add(b.self(0, 3))
	}
	status := 200
	if name == "POST /cart" || name == "POST /api/v1/orders" {
		status = 201
	}
	if b.failed && b.rng.Intn(5) > 0 {
		status = pick2(b.rng, 500, 502)
		span.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR}
	} else if b.rng.Intn(100) < 3 {
		status = pick2(b.rng, 404, 400)
	}
	span.Attributes = append(span.Attributes, intAttr("http.response.status_code", int64(status)))
	span.EndTimeUnixNano = uint64(end.UnixNano())
	return end
}

// call is a CLIENT span of from calling service's route, and that service's
// SERVER span running body.
func (b *traceBuilder) call(
	parent *tracev1.Span, from, service, method, route, path string, at time.Time,
	body func(*tracev1.Span, time.Time) time.Time,
) time.Time {
	client := b.span(parent, from, method+" "+route, tracev1.Span_SPAN_KIND_CLIENT, at)
	namespace := b.services[service].namespace
	client.Attributes = append(client.Attributes, strAttr("http.request.method", method),
		strAttr("server.address", service+"."+namespace+".svc"), strAttr("peer.service", service),
		strAttr("url.full", "http://"+service+"."+namespace+".svc:8080"+path))
	end := b.server(client, service, method+" "+route, path, at.Add(b.self(0, 2)), body)
	end = end.Add(b.self(0, 2))
	client.Attributes = append(client.Attributes, intAttr("http.response.status_code", int64(statusOf(b, client))))
	client.EndTimeUnixNano = uint64(end.UnixNano())
	return end
}

func (b *traceBuilder) db(parent *tracev1.Span, service, operation, statement string, at time.Time) time.Time {
	span := b.span(parent, service, operation+" shop", tracev1.Span_SPAN_KIND_CLIENT, at)
	span.Attributes = append(span.Attributes, strAttr("db.system.name", "postgresql"),
		strAttr("db.namespace", "shop"), strAttr("db.operation.name", operation), strAttr("db.query.text", statement),
		strAttr("server.address", "postgres.data.svc"))
	return b.leaf(
		span,
		at,
		1,
		25,
		"org.postgresql.util.PSQLException: ERROR: canceling statement due to statement timeout",
	)
}

func (b *traceBuilder) cache(parent *tracev1.Span, service, command, key string, at time.Time) time.Time {
	span := b.span(parent, service, command, tracev1.Span_SPAN_KIND_CLIENT, at)
	span.Attributes = append(span.Attributes, strAttr("db.system.name", "redis"),
		strAttr("db.operation.name", command), strAttr("db.query.text", command+" "+key),
		strAttr("server.address", "redis.data.svc"))
	return b.leaf(span, at, 0, 3, "redis: connection pool timeout")
}

func (b *traceBuilder) publish(parent *tracev1.Span, service, topic, key string, at time.Time) time.Time {
	span := b.span(parent, service, "send "+topic, tracev1.Span_SPAN_KIND_PRODUCER, at)
	span.Attributes = append(span.Attributes, strAttr("messaging.system", "kafka"),
		strAttr("messaging.destination.name", topic), strAttr("messaging.operation.type", "send"),
		strAttr("messaging.kafka.message.key", key))
	return b.leaf(span, at, 1, 8, "org.apache.kafka.common.errors.TimeoutException: Expiring 1 record(s)")
}

func (b *traceBuilder) external(parent *tracev1.Span, service, method, host, path string, at time.Time) time.Time {
	span := b.span(parent, service, method, tracev1.Span_SPAN_KIND_CLIENT, at)
	span.Attributes = append(span.Attributes, strAttr("http.request.method", method),
		strAttr("server.address", host), strAttr("url.full", "https://"+host+path))
	end := b.leaf(span, at, 20, 180, "java.net.SocketTimeoutException: Read timed out")
	span.Attributes = append(span.Attributes, intAttr("http.response.status_code", int64(statusOf(b, span))))
	return end
}

// consume is a CONSUMER span starting its own trace for a Kafka record.
func (b *traceBuilder) consume(
	service, topic, key string,
	at time.Time,
	body func(*tracev1.Span, time.Time) time.Time,
) {
	span := b.span(nil, service, "process "+topic, tracev1.Span_SPAN_KIND_CONSUMER, at)
	span.Attributes = append(span.Attributes, strAttr("messaging.system", "kafka"),
		strAttr("messaging.destination.name", topic), strAttr("messaging.operation.type", "process"),
		strAttr("messaging.kafka.message.key", key), strAttr("messaging.consumer.group.name", service))
	end := body(span, at.Add(b.self(0, 2))).Add(b.self(0, 2))
	if b.failed {
		span.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR}
	}
	span.EndTimeUnixNano = uint64(end.UnixNano())
}

// leaf ends a call that does no further calls: usually fast, sometimes slow,
// rarely failing with an exception event.
func (b *traceBuilder) leaf(span *tracev1.Span, at time.Time, minMs, maxMs int, exception string) time.Time {
	duration := b.self(minMs, maxMs)
	if b.rng.Intn(50) == 0 {
		duration = time.Duration(500+b.rng.Intn(2500)) * time.Millisecond
	}
	end := at.Add(duration)
	if b.rng.Intn(100) < 2 {
		b.failed = true
		span.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR, Message: exception}
		frames := ""
		for i := range 4 + b.rng.Intn(4) {
			frames += "\n\tat " + fmt.Sprintf(k8sFrames[i%len(k8sFrames)], 40+b.rng.Intn(400))
		}
		span.Events = append(span.Events, &tracev1.Span_Event{
			TimeUnixNano: uint64(end.UnixNano()),
			Name:         "exception",
			Attributes: []*commonv1.KeyValue{
				strAttr("exception.type", exceptionType(exception)),
				strAttr("exception.message", exception),
				strAttr("exception.stacktrace", exception+frames),
			},
		})
	}
	span.EndTimeUnixNano = uint64(end.UnixNano())
	return end
}

// span starts a span of service at at, on one of its pods: mostly the one
// its last span used, so a batch has a few resources per service.
func (b *traceBuilder) span(
	parent *tracev1.Span,
	service, name string,
	kind tracev1.Span_SpanKind,
	at time.Time,
) *tracev1.Span {
	svc := b.services[service]
	pod, ok := b.current[service]
	if !ok || b.rng.Intn(8) == 0 {
		pod = svc.podAt(b.rng.Intn(svc.replicas), b.at)
		b.current[service] = pod
	}
	b.pods[pod.name] = pod
	span := &tracev1.Span{
		TraceId:           b.traceID,
		SpanId:            randomBytes(b.rng, 8),
		Name:              name,
		Kind:              kind,
		StartTimeUnixNano: uint64(at.UnixNano()),
		EndTimeUnixNano:   uint64(at.UnixNano()),
	}
	if parent != nil {
		span.ParentSpanId = parent.SpanId
	}
	b.spans = append(b.spans, span)
	b.podOf = append(b.podOf, pod.name)
	return span
}

func (b *traceBuilder) self(minMs, maxMs int) time.Duration {
	return time.Duration(minMs*1000+b.rng.Intn((maxMs-minMs)*1000+1)) * time.Microsecond
}

func pickFlow(rng *rand.Rand) traceFlow {
	total := 0
	for _, flow := range k8sTraceFlows {
		total += flow.weight
	}
	n := rng.Intn(total)
	for _, flow := range k8sTraceFlows {
		if n < flow.weight {
			return flow
		}
		n -= flow.weight
	}
	return k8sTraceFlows[0]
}

// statusOf is a client span's response status: 5xx once its call failed.
func statusOf(b *traceBuilder, span *tracev1.Span) int {
	if span.Status != nil && span.Status.Code == tracev1.Status_STATUS_CODE_ERROR {
		return 503
	}
	if b.failed {
		span.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR}
		return 500
	}
	return 200
}

func splitName(name string) (string, string) {
	for i := range len(name) {
		if name[i] == ' ' {
			return name[:i], name[i+1:]
		}
	}
	return name, ""
}

func exceptionType(message string) string {
	for i := range len(message) {
		if message[i] == ':' {
			return message[:i]
		}
	}
	return message
}

func randomBytes(rng *rand.Rand, n int) []byte {
	out := make([]byte, n)
	rng.Read(out)
	return out
}
