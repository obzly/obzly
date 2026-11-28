package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// The k8s profile sends logs shaped like a busy Kubernetes cluster's: many
// pods that come and go, request logs full of IDs, JSON and key=value lines,
// web access logs, stack traces and multi-KB payloads. Every name, address
// and value is made up (Contoso, example.com, documentation IP ranges).
//
// Timestamps follow a simulated clock: line i is stamped
// start + i/linesPerSecond. With -backfill the clock starts in the past and
// batches go out as fast as the server takes them until it reaches now; from
// then on each batch waits for its own timestamp, so the rate settles at
// linesPerSecond whatever -concurrency is.

type k8sLineKind int

const (
	kindProbe k8sLineKind = iota
	kindAccess
	kindJSONRequest
	kindKeyValue
	kindFreeText
	kindEmail
	kindSlowQuery
	kindKafka
	kindSlowUpstream
	kindPayload
	kindStackTrace
	kindPrettyJSON
)

// k8sService is one Deployment: its pods churn every churn period (an
// autoscaler or a rollout), so pod names keep changing like a real cluster's.
type k8sService struct {
	name      string
	namespace string
	replicas  int
	churn     time.Duration
	kinds     []weightedKind
}

type weightedKind struct {
	kind   k8sLineKind
	weight int
}

type k8sPod struct {
	service *k8sService
	name    string
	node    string
}

type simClock struct {
	start   time.Time
	perLine float64
	next    atomic.Int64
}

// k8sLogsSender sends a simulated-clock profile's batches (k8s or cluster
// logs, k8s traces or metrics), built by build, retrying the ones the Site
// pushes back on. The clock counts the signal's items: log lines, spans or
// data points.
type k8sLogsSender struct {
	otlp    *otlpSender
	clock   *simClock
	profile string
	build   func(rng *rand.Rand, first int64, n int) proto.Message
	retries atomic.Int64
	// dropped: batches the Site refused for good (a 4xx other than 429),
	// which a collector would drop too. Counted so a drill can see loss on
	// the sending side.
	dropped atomic.Int64
	// rejected: lines the Site answered 200 for but refused in an OTLP
	// partial success; a collector logs and drops them without a retry.
	rejected atomic.Int64
	backoff  time.Duration
}

var (
	k8sWebKinds = []weightedKind{
		{kindAccess, 70}, {kindProbe, 20}, {kindSlowUpstream, 6}, {kindFreeText, 4},
	}
	k8sAPIKinds = []weightedKind{
		{kindJSONRequest, 34}, {kindKeyValue, 18}, {kindProbe, 14}, {kindFreeText, 12},
		{kindEmail, 5}, {kindSlowUpstream, 5}, {kindPayload, 6}, {kindStackTrace, 3}, {kindPrettyJSON, 3},
	}
	k8sWorkerKinds = []weightedKind{
		{kindKafka, 30}, {kindKeyValue, 22}, {kindSlowQuery, 14}, {kindFreeText, 12},
		{kindPayload, 8}, {kindProbe, 8}, {kindStackTrace, 3}, {kindPrettyJSON, 3},
	}

	k8sAdjectives = []string{
		"Blue", "Classic", "Coastal", "Everyday", "Heritage", "Midnight", "Alpine", "Urban", "Vintage", "Summit",
		"Harbour", "Meadow", "Granite", "Willow", "Copper", "Ember", "Glacier", "Saffron", "Juniper", "Linen",
	}
	k8sMaterials = []string{
		"Merino", "Cotton", "Linen", "Leather", "Bamboo", "Cedar", "Ceramic", "Steel", "Wool", "Canvas",
		"Oak", "Silk", "Denim", "Cork", "Hemp",
	}
	k8sItems = []string{
		"Scarf", "Throw", "Mug", "Backpack", "Beanie", "Tote", "Lamp", "Notebook", "Planter", "Jacket",
		"Blanket", "Cushion", "Wallet", "Bottle", "Apron", "Candle", "Rug", "Basket", "Hoodie", "Sneaker",
		"Socks", "Gloves", "Belt", "Tray", "Vase", "Stool", "Clock", "Frame", "Kettle", "Pan",
	}
	k8sCategories = []string{
		"Winter Accessories", "Home & Living", "Kitchen", "Outdoor", "Bags", "Footwear", "Stationery", "Gifts",
	}
	k8sFirstNames = []string{
		"alex", "sam", "jordan", "taylor", "casey", "riley", "morgan", "jamie", "avery", "quinn",
		"harper", "rowan", "sage", "emery", "kai", "noa", "ari", "reese", "parker", "skyler",
	}
	k8sLastNames = []string{
		"smith", "lee", "patel", "nguyen", "garcia", "brown", "wilson", "khan", "martin", "walker",
		"young", "king", "wright", "lopez", "hill", "scott", "green", "adams", "baker", "hall",
	}
	k8sEmailDomains = []string{"example.com", "example.net", "example.org"}
	k8sUserAgents   = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148",
		"Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Mobile Safari/537.36",
		"Mozilla/5.0 (X11; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0",
		"Mozilla/5.0 (compatible; ContosoBot/2.1; +https://www.example.com/bot)",
		"curl/8.11.0",
	}
	k8sReferrers = []string{
		"-", "https://www.example.com/", "https://www.example.com/collections/winter",
		"https://search.example.net/?q=contoso", "https://social.example.org/",
	}
	k8sRoutes = []string{
		"/api/v1/orders/{id}", "/api/v1/carts/{id}/items", "/api/v1/products/{id}", "/api/v1/customers/{id}/addresses",
		"/api/v1/payments/{id}/capture", "/api/v1/search", "/api/v1/sessions", "/api/v1/inventory/{id}",
	}
	k8sUpstreams = []string{
		"inventory-api",
		"pricing-api",
		"card-gateway",
		"tax-service",
		"shipping-rates",
		"recommendations",
		"identity-api",
	}
	k8sTopics = []string{"orders.v1", "payments.v1", "inventory.v2", "customer-events", "search-index", "email-outbox"}
	k8sTables = []string{"orders", "order_items", "payments", "customers", "inventory_levels", "shipments"}
	k8sEvents = []string{
		"order.created",
		"order.updated",
		"payment.captured",
		"shipment.dispatched",
		"refund.issued",
		"customer.updated",
	}
	k8sExceptions = []string{
		"java.net.SocketTimeoutException: Read timed out",
		"java.lang.IllegalStateException: Cart already checked out",
		"org.springframework.dao.DataIntegrityViolationException: duplicate key value violates unique constraint",
		"java.lang.NullPointerException: Cannot invoke \"Address.getPostcode()\" because \"shipping\" is null",
		"io.grpc.StatusRuntimeException: UNAVAILABLE: io exception",
	}
	k8sFrames = []string{
		"com.contoso.payments.gateway.CardGatewayClient.authorize(CardGatewayClient.java:%d)",
		"com.contoso.payments.service.PaymentService.capture(PaymentService.java:%d)",
		"com.contoso.orders.checkout.CheckoutHandler.handle(CheckoutHandler.java:%d)",
		"com.contoso.common.http.RetryingClient.execute(RetryingClient.java:%d)",
		"org.springframework.web.servlet.FrameworkServlet.service(FrameworkServlet.java:%d)",
		"org.apache.catalina.core.ApplicationFilterChain.doFilter(ApplicationFilterChain.java:%d)",
		"java.base/java.util.concurrent.ThreadPoolExecutor.runWorker(ThreadPoolExecutor.java:%d)",
		"java.base/java.lang.Thread.run(Thread.java:%d)",
	}
)

const (
	alnumUpper  = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	alnumLower  = "abcdefghijkmnpqrstuvwxyz23456789"
	podSuffixes = "bcdfghjklmnpqrstvwxz2456789"
	hexDigits   = "0123456789abcdef"
)

func newK8sLogsSender(otlp *otlpSender, start time.Time, linesPerSecond float64, pods int) *k8sLogsSender {
	clock := &simClock{start: start, perLine: float64(time.Second) / linesPerSecond}
	services := k8sServices(pods)
	return &k8sLogsSender{
		otlp:    otlp,
		clock:   clock,
		profile: "k8s",
		build: func(rng *rand.Rand, first int64, n int) proto.Message {
			return buildK8sLogsRequest(rng, services, clock, first, n, 4)
		},
		backoff: 250 * time.Millisecond,
	}
}

func (s *k8sLogsSender) send(ctx context.Context, rng *rand.Rand) result {
	first := s.clock.reserve(s.otlp.batchSize)
	if wait := time.Until(s.clock.at(first)); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return result{err: ctx.Err()}
		}
	}
	request := s.build(rng, first, s.otlp.batchSize)
	// Like an OTel Collector, keep a batch the Site pushes back on (429, 5xx,
	// a replica going away) and send it again, so a backfill has no gaps and
	// settles at the rate the Site sustains.
	wait := s.backoff
	for {
		var r result
		if s.otlp.protocol == "grpc" {
			attempt, cancel := context.WithTimeout(ctx, s.otlp.timeout)
			r = s.otlp.sendGRPC(attempt, request)
			cancel()
		} else {
			r = s.otlp.sendHTTP(ctx, request)
		}
		if r.err == nil && r.rejected > 0 {
			s.rejected.Add(int64(r.rejected))
		}
		if r.err == nil || ctx.Err() != nil {
			return r
		}
		if !retryable(r) {
			s.dropped.Add(1)
			fmt.Printf("%s  dropped a batch of %d %s at simulated %s: HTTP %d %v\n",
				time.Now().UTC().Format(time.RFC3339), s.otlp.batchSize, s.unit(),
				s.clock.at(first).UTC().Format(time.RFC3339), r.httpStatusCode, r.err)
			return r
		}
		s.retries.Add(1)
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return r
		}
		wait = min(2*wait, 10*time.Second)
	}
}

func (s *k8sLogsSender) target() string { return s.otlp.target() + " (" + s.profile + " profile)" }
func (s *k8sLogsSender) unit() string   { return s.otlp.unit() }
func (s *k8sLogsSender) close()         { s.otlp.close() }

// progress reports how far the simulated clock has got, for long backfills.
func (s *k8sLogsSender) progress() string {
	lines := s.clock.next.Load()
	at := s.clock.at(lines)
	behind := time.Since(at).Round(time.Second)
	if behind < 0 {
		behind = 0
	}
	return fmt.Sprintf(
		"simulated clock %s (%s behind now), %d %s, %d retries, %d batches dropped, %d %s rejected in partial successes",
		at.UTC().
			Format(time.RFC3339),
		behind,
		lines,
		s.unit(),
		s.retries.Load(),
		s.dropped.Load(),
		s.rejected.Load(),
		s.unit(),
	)
}

// retryable: the Site pushed back or couldn't be reached; anything else (a
// bad request, a wrong API key) would fail again.
func retryable(r result) bool {
	switch {
	case r.httpStatusCode == 0 && r.err != nil:
		for _, final := range []string{"grpc Unauthenticated", "grpc PermissionDenied", "grpc InvalidArgument", "marshal"} {
			if strings.HasPrefix(r.err.Error(), final) {
				return false
			}
		}
		return true
	case r.httpStatusCode == http.StatusTooManyRequests, r.httpStatusCode >= 500:
		return true
	default:
		return false
	}
}

func (c *simClock) reserve(n int) int64 {
	return c.next.Add(int64(n)) - int64(n)
}

func (c *simClock) at(line int64) time.Time {
	return c.start.Add(time.Duration(float64(line) * c.perLine))
}

// k8sServices spreads about `pods` replicas over 24 Deployments in the
// namespaces above; each churns its pods on its own period (2 to 26 hours).
func k8sServices(pods int) []*k8sService {
	type spec struct {
		name, namespace string
		kinds           []weightedKind
		share           int
	}
	specs := []spec{
		{"storefront-web", "edge", k8sWebKinds, 8}, {"checkout-web", "edge", k8sWebKinds, 4},
		{"admin-web", "edge", k8sWebKinds, 1}, {"cdn-origin", "edge", k8sWebKinds, 3},
		{"orders-api", "shop", k8sAPIKinds, 6}, {"carts-api", "shop", k8sAPIKinds, 5},
		{"catalog-api", "shop", k8sAPIKinds, 6}, {"pricing-api", "shop", k8sAPIKinds, 3},
		{"inventory-api", "shop", k8sAPIKinds, 4}, {"payments-api", "payments", k8sAPIKinds, 4},
		{"card-gateway", "payments", k8sAPIKinds, 3}, {"refunds-worker", "payments", k8sWorkerKinds, 2},
		{"identity-api", "identity", k8sAPIKinds, 3}, {"sessions-api", "identity", k8sAPIKinds, 3},
		{"search-api", "search", k8sAPIKinds, 5}, {"search-indexer", "search", k8sWorkerKinds, 3},
		{"recommendations", "search", k8sAPIKinds, 3}, {"order-projector", "data", k8sWorkerKinds, 3},
		{"email-sender", "platform", k8sWorkerKinds, 2}, {"shipping-worker", "data", k8sWorkerKinds, 3},
		{"tax-service", "payments", k8sAPIKinds, 2}, {"audit-writer", "platform", k8sWorkerKinds, 2},
		{"feature-flags", "platform", k8sAPIKinds, 1}, {"reporting-etl", "data", k8sWorkerKinds, 2},
	}
	total := 0
	for _, sp := range specs {
		total += sp.share
	}
	services := make([]*k8sService, len(specs))
	for i, sp := range specs {
		replicas := max(1, int(math.Round(float64(pods*sp.share)/float64(total))))
		services[i] = &k8sService{
			name:      sp.name,
			namespace: sp.namespace,
			replicas:  replicas,
			churn:     time.Duration(2+(i*7)%25) * time.Hour,
			kinds:     sp.kinds,
		}
	}
	return services
}

// podAt names a service's replica at a moment: the ReplicaSet hash and pod
// suffix are derived from the churn epoch, so the same moment always gives
// the same pod and no state is kept.
func (svc *k8sService) podAt(replica int, at time.Time) k8sPod {
	epoch := at.UnixNano() / int64(svc.churn)
	rs := stableToken(fmt.Sprintf("%s/rs/%d", svc.name, epoch/6), alnumLower, 10)
	suffix := stableToken(fmt.Sprintf("%s/%d/%d", svc.name, replica, epoch), podSuffixes, 5)
	node := 1 + int(stableUint(fmt.Sprintf("%s/%d/%d/node", svc.name, replica, epoch))%12)
	return k8sPod{
		service: svc,
		name:    fmt.Sprintf("%s-%s-%s", svc.name, rs, suffix),
		node:    fmt.Sprintf("contoso-node-%02d", node),
	}
}

func buildK8sLogsRequest(
	rng *rand.Rand,
	services []*k8sService,
	clock *simClock,
	first int64,
	n, podsPer int,
) *collectorlogsv1.ExportLogsServiceRequest {
	at := clock.at(first)
	pods := make([]k8sPod, podsPer)
	for i := range pods {
		svc := pickService(rng, services)
		pods[i] = svc.podAt(rng.Intn(svc.replicas), at)
	}
	records := make([][]*logsv1.LogRecord, podsPer)
	for i := 0; i < n; i++ {
		p := rng.Intn(podsPer)
		ts := clock.at(first + int64(i))
		level, body := k8sLine(rng, pickKind(rng, pods[p].service.kinds), pods[p], ts)
		records[p] = append(records[p], &logsv1.LogRecord{
			TimeUnixNano:   uint64(ts.UnixNano()),
			SeverityText:   level,
			SeverityNumber: otlpSeverities[level],
			Body:           &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: body}},
		})
	}
	request := &collectorlogsv1.ExportLogsServiceRequest{}
	for i, pod := range pods {
		if len(records[i]) == 0 {
			continue
		}
		request.ResourceLogs = append(request.ResourceLogs, &logsv1.ResourceLogs{
			Resource:  k8sResource(pod),
			ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: records[i]}},
		})
	}
	return request
}

func k8sResource(pod k8sPod) *resourcev1.Resource {
	return &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
		strAttr("service.name", pod.service.name),
		strAttr("k8s.namespace.name", pod.service.namespace),
		strAttr("k8s.deployment.name", pod.service.name),
		strAttr("k8s.pod.name", pod.name),
		strAttr("k8s.container.name", pod.service.name),
		strAttr("k8s.node.name", pod.node),
		strAttr("k8s.cluster.name", "contoso-lab"),
		strAttr("loadtest", "k8s"),
	}}
}

func k8sLine(rng *rand.Rand, kind k8sLineKind, pod k8sPod, ts time.Time) (string, string) {
	switch kind {
	case kindProbe:
		return "DEBUG", fmt.Sprintf("GET /healthz 200 %.1fms", 0.2+rng.Float64()*2)
	case kindAccess:
		return accessLine(rng, ts)
	case kindJSONRequest:
		return jsonRequestLine(rng, ts)
	case kindKeyValue:
		return keyValueLine(rng, ts)
	case kindFreeText:
		return freeTextLine(rng)
	case kindEmail:
		return "INFO", fmt.Sprintf("Sent %s to %s for order %s",
			pick(rng, []string{"order confirmation", "shipping update", "password reset link", "receipt"}),
			email(rng), orderID(rng))
	case kindSlowQuery:
		return slowQueryLine(rng)
	case kindKafka:
		return "INFO", fmt.Sprintf(
			"[Consumer clientId=consumer-%s-%d, groupId=%s] Committed offset %d for partition %s-%d",
			pod.service.name,
			rng.Intn(12),
			pod.service.name,
			1_000_000+rng.Intn(90_000_000),
			pick(rng, k8sTopics),
			rng.Intn(24),
		)
	case kindSlowUpstream:
		return "WARN", fmt.Sprintf("level=warn msg=\"slow upstream\" upstream=%s took=%.3fs retries=%d request_id=%s",
			pick(rng, k8sUpstreams), 0.8+rng.Float64()*4, rng.Intn(4), hexToken(rng, 16))
	case kindPayload:
		return payloadLine(rng, ts)
	case kindStackTrace:
		return stackTraceLine(rng)
	default:
		return prettyJSONLine(rng, ts)
	}
}

func accessLine(rng *rand.Rand, ts time.Time) (string, string) {
	status := pickWeighted(rng, []int{200, 200, 200, 200, 200, 200, 304, 301, 404, 404, 499, 500, 502})
	path := pick(rng, []string{
		"/", "/collections/winter", "/collections/home-living",
		fmt.Sprintf("/products/sku-%05d", rng.Intn(40_000)),
		fmt.Sprintf("/products/%s", productSlug(rng)),
		fmt.Sprintf("/cart/%s", uuidToken(rng)),
		fmt.Sprintf("/search?q=%s&page=%d", strings.ToLower(pick(rng, k8sItems)), 1+rng.Intn(5)),
		"/checkout", "/account/orders", "/static/app." + hexToken(rng, 8) + ".js",
		"/static/styles." + hexToken(rng, 8) + ".css", "/images/" + productSlug(rng) + ".webp",
		"/favicon.ico", "/robots.txt",
	})
	method := "GET"
	if rng.Intn(12) == 0 {
		method = "POST"
	}
	level := "INFO"
	if status >= 500 {
		level = "ERROR"
	}
	return level, fmt.Sprintf("%s - - [%s] \"%s %s HTTP/1.1\" %d %d \"%s\" \"%s\" %.3f",
		docIP(rng), ts.UTC().Format("02/Jan/2006:15:04:05 -0700"), method, path, status, 200+rng.Intn(60_000),
		pick(rng, k8sReferrers), pick(rng, k8sUserAgents), rng.Float64()*1.5)
}

func jsonRequestLine(rng *rand.Rand, ts time.Time) (string, string) {
	route := pick(rng, k8sRoutes)
	status := pickWeighted(rng, []int{200, 200, 200, 200, 201, 204, 400, 401, 404, 409, 500})
	level := "info"
	if status >= 500 {
		level = "error"
	} else if status >= 400 {
		level = "warn"
	}
	return strings.ToUpper(level), fmt.Sprintf(
		`{"level":"%s","ts":"%s","msg":"request completed","method":"%s","route":"%s","path":"%s","status":%d,"duration_ms":%.1f,"user_id":%s,"tenant":"%s","trace_id":"%s","span_id":"%s"}`,
		level,
		ts.UTC().Format(time.RFC3339Nano),
		pick(rng, []string{"GET", "GET", "GET", "POST", "PUT", "DELETE"}),
		route,
		strings.Replace(route, "{id}", idToken(rng, route), 1),
		status,
		rng.Float64()*400,
		userID(rng),
		pick(rng, []string{"contoso-nz", "contoso-au", "fabrikam-us", "fabrikam-eu"}),
		hexToken(rng, 32),
		hexToken(rng, 16),
	)
}

func keyValueLine(rng *rand.Rand, ts time.Time) (string, string) {
	switch rng.Intn(4) {
	case 0:
		return "INFO", fmt.Sprintf(
			`ts=%s level=info msg="payment authorized" payment_id=pay_%d%s amount=%.2f currency=%s card_brand=%s merchant=contoso-shop region=%s attempt=%d`,
			ts.UTC().
				Format(time.RFC3339),
			rng.Intn(10),
			token(rng, alnumUpper, 9),
			5+rng.Float64()*900,
			pick(rng, []string{"NZD", "AUD", "USD", "EUR"}),
			pick(
				rng,
				[]string{"VISA", "MASTERCARD", "AMEX"},
			),
			pick(rng, []string{"ap-southeast-2", "us-east-1", "eu-west-1"}),
			1+rng.Intn(3),
		)
	case 1:
		return "INFO", fmt.Sprintf(
			`level=info msg="order placed" order_id=%s customer=cust_%s items=%d total=%.2f channel=%s`,
			orderID(
				rng,
			),
			token(rng, alnumLower, 9),
			1+rng.Intn(9),
			10+rng.Float64()*1200,
			pick(rng, []string{"web", "ios", "android", "pos"}),
		)
	case 2:
		return "INFO", fmt.Sprintf(
			`level=info msg="inventory reserved" sku=sku-%05d warehouse=%s qty=%d reservation=%s ttl=%dm`,
			rng.Intn(
				40_000,
			),
			pick(rng, []string{"akl-1", "syd-2", "mel-1", "chc-1"}),
			1+rng.Intn(6),
			uuidToken(rng),
			5+rng.Intn(25),
		)
	default:
		return "WARN", fmt.Sprintf(
			`level=warn msg="rate limit close" client=%s used=%d limit=%d window=60s key=%s`,
			pick(
				rng,
				[]string{"partner-fabrikam", "mobile-app", "storefront", "batch-import"},
			),
			800+rng.Intn(200),
			1000,
			hexToken(rng, 24),
		)
	}
}

// freeTextLine carries values no masking rule can recognise (product and
// category names, plain words), like real application logs.
func freeTextLine(rng *rand.Rand) (string, string) {
	product := productName(rng)
	switch rng.Intn(4) {
	case 0:
		return "INFO", fmt.Sprintf(`Cache miss for product "%s" in category "%s"`, product, pick(rng, k8sCategories))
	case 1:
		return "INFO", fmt.Sprintf("Recomputed recommendations for %s: %d products, top pick %s",
			pick(rng, k8sCategories), 5+rng.Intn(40), product)
	case 2:
		return "WARN", fmt.Sprintf(
			"Low stock for %s at %s warehouse",
			product,
			pick(rng, []string{"Auckland", "Sydney", "Melbourne", "Christchurch"}),
		)
	default:
		return "INFO", fmt.Sprintf(
			"Price updated for %s by %s",
			product,
			pick(rng, []string{"pricing rules", "a merchandiser", "the weekend sale", "a supplier feed"}),
		)
	}
}

func slowQueryLine(rng *rand.Rand) (string, string) {
	table := pick(rng, k8sTables)
	return "WARN", fmt.Sprintf(
		"duration: %.3f ms  execute <unnamed>: SELECT id, status, updated_at FROM %s WHERE customer_id = $1 AND created_at > $2 ORDER BY created_at DESC LIMIT %d\nDETAIL:  parameters: $1 = '%d', $2 = '2026-%02d-%02d %02d:%02d:%02d'",
		500+rng.Float64()*9000,
		table,
		10*(1+rng.Intn(10)),
		rng.Intn(5_000_000),
		1+rng.Intn(12),
		1+rng.Intn(28),
		rng.Intn(24),
		rng.Intn(60),
		rng.Intn(60),
	)
}

func payloadLine(rng *rand.Rand, ts time.Time) (string, string) {
	var b strings.Builder
	fmt.Fprintf(
		&b,
		`Webhook delivered to https://hooks.example.com/%s status=%d payload={"event":"%s","sent_at":"%s","order":{"id":"%s","customer":{"id":"cust_%s","email":"%s","name":"%s %s"},"items":[`,
		token(rng, alnumLower, 12),
		pickWeighted(rng, []int{200, 200, 200, 202, 500}),
		pick(rng, k8sEvents),
		ts.UTC().Format(time.RFC3339Nano),
		orderID(rng),
		token(rng, alnumLower, 9),
		email(rng),
		pick(rng, k8sFirstNames),
		pick(rng, k8sLastNames),
	)
	items := 3 + rng.Intn(25)
	for i := 0; i < items; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"sku":"sku-%05d","name":"%s","qty":%d,"price":%.2f,"gift":%t}`,
			rng.Intn(40_000), productName(rng), 1+rng.Intn(4), 5+rng.Float64()*300, rng.Intn(10) == 0)
	}
	fmt.Fprintf(
		&b,
		`],"shipping":{"method":"%s","postcode":"%04d","notes":"%s"}}}`,
		pick(rng, []string{"standard", "express", "click-and-collect"}),
		rng.Intn(10_000),
		pick(
			rng,
			[]string{"Leave at the door", "Call on arrival", "", "Gift wrap please", "Back entrance via the lane"},
		),
	)
	return "INFO", b.String()
}

// stackTraceLine throws from one of a few fixed sites: the same exception
// always has the same frames, as in a real service.
func stackTraceLine(rng *rand.Rand) (string, string) {
	site := rng.Intn(len(k8sExceptions) * 3)
	var b strings.Builder
	fmt.Fprintf(
		&b,
		"ERROR c.c.payments.CardGatewayClient - Card authorization failed for pay_%d%s after %d attempts\n%s",
		rng.Intn(10),
		token(rng, alnumUpper, 9),
		1+rng.Intn(3),
		k8sExceptions[site%len(k8sExceptions)],
	)
	frameRng := rand.New(rand.NewSource(int64(site)))
	frames := 8 + frameRng.Intn(20)
	for i := 0; i < frames; i++ {
		fmt.Fprintf(&b, "\n\tat "+k8sFrames[frameRng.Intn(len(k8sFrames))], 20+frameRng.Intn(900))
	}
	return "ERROR", b.String()
}

func prettyJSONLine(rng *rand.Rand, ts time.Time) (string, string) {
	return "INFO", fmt.Sprintf(
		"{\n  \"event\": \"%s\",\n  \"at\": \"%s\",\n  \"order_id\": \"%s\",\n  \"customer\": {\n    \"id\": \"cust_%s\",\n    \"segment\": \"%s\"\n  },\n  \"total\": %.2f,\n  \"note\": \"%s\"\n}",
		pick(rng, k8sEvents),
		ts.UTC().Format(time.RFC3339),
		orderID(rng),
		token(rng, alnumLower, 9),
		pick(rng, []string{"new", "returning", "vip", "wholesale"}),
		10+rng.Float64()*900,
		productName(rng),
	)
}

func pickService(rng *rand.Rand, services []*k8sService) *k8sService {
	total := 0
	for _, s := range services {
		total += s.replicas
	}
	n := rng.Intn(total)
	for _, s := range services {
		if n < s.replicas {
			return s
		}
		n -= s.replicas
	}
	return services[len(services)-1]
}

func pickKind(rng *rand.Rand, kinds []weightedKind) k8sLineKind {
	total := 0
	for _, k := range kinds {
		total += k.weight
	}
	n := rng.Intn(total)
	for _, k := range kinds {
		if n < k.weight {
			return k.kind
		}
		n -= k.weight
	}
	return kinds[len(kinds)-1].kind
}

func pick(rng *rand.Rand, values []string) string { return values[rng.Intn(len(values))] }

func pickWeighted(rng *rand.Rand, values []int) int { return values[rng.Intn(len(values))] }

// productName favours a few best sellers (Zipf) over the 9,000 products,
// like a real catalogue's traffic.
func productName(rng *rand.Rand) string {
	n := len(k8sAdjectives) * len(k8sMaterials) * len(k8sItems)
	i := int(rand.NewZipf(rng, 1.5, 2, uint64(n-1)).Uint64())
	return k8sAdjectives[i%len(k8sAdjectives)] + " " +
		k8sMaterials[(i/len(k8sAdjectives))%len(k8sMaterials)] + " " +
		k8sItems[i/(len(k8sAdjectives)*len(k8sMaterials))]
}

func productSlug(rng *rand.Rand) string {
	return strings.ToLower(strings.ReplaceAll(productName(rng), " ", "-"))
}

func email(rng *rand.Rand) string {
	name := pick(rng, k8sFirstNames) + "." + pick(rng, k8sLastNames)
	if rng.Intn(3) == 0 {
		name += fmt.Sprintf("+%d", rng.Intn(1000))
	}
	return name + "@" + pick(rng, k8sEmailDomains)
}

func orderID(rng *rand.Rand) string {
	if rng.Intn(5) == 0 {
		return "ORD" + token(rng, alnumUpper, 8)
	}
	return fmt.Sprintf("ORD-%08d", rng.Intn(100_000_000))
}

func userID(rng *rand.Rand) string {
	if rng.Intn(10) < 7 {
		return fmt.Sprintf("%d", 100_000+rng.Intn(9_900_000))
	}
	return `"usr_` + token(rng, alnumLower, 8) + `"`
}

func idToken(rng *rand.Rand, route string) string {
	switch {
	case strings.Contains(route, "orders"):
		return orderID(rng)
	case strings.Contains(route, "products"), strings.Contains(route, "inventory"):
		return fmt.Sprintf("sku-%05d", rng.Intn(40_000))
	case strings.Contains(route, "payments"):
		return fmt.Sprintf("pay_%d%s", rng.Intn(10), token(rng, alnumUpper, 9))
	default:
		return uuidToken(rng)
	}
}

// docIP is an address in the three documentation ranges (RFC 5737).
func docIP(rng *rand.Rand) string {
	return fmt.Sprintf("%s.%d", pick(rng, []string{"192.0.2", "198.51.100", "203.0.113"}), 1+rng.Intn(254))
}

func token(rng *rand.Rand, alphabet string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(b)
}

func hexToken(rng *rand.Rand, n int) string { return token(rng, hexDigits, n) }

func uuidToken(rng *rand.Rand) string {
	h := hexToken(rng, 32)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func stableUint(key string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return h.Sum64()
}

func stableToken(key, alphabet string, n int) string {
	return token(rand.New(rand.NewSource(int64(stableUint(key)))), alphabet, n)
}
