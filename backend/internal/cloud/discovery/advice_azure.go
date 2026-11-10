package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"obzly/internal/synthetics/spec"
)

const (
	azureAdvisorAPIVersion      = "2023-01-01"
	azureReservationRecsVersion = "2023-05-01"
	azureBenefitRecsVersion     = "2023-08-01"
	azureCapacityAPIVersion     = "2022-11-01"
	azureBenefitsAPIVersion     = "2022-11-01"
	azureMaxPages               = 50
)

var azureTerms = []struct{ term, api string }{{spec.Term1Year, "P1Y"}, {spec.Term3Years, "P3Y"}}

// Obzly's checks for resources nobody uses; they need Reader, which Inventory or Posture gives.
const azureIdleQuery = `resources
| where (type =~ 'microsoft.compute/disks' and tostring(properties.diskState) =~ 'Unattached')
  or (type =~ 'microsoft.network/publicipaddresses' and isnull(properties.ipConfiguration)
      and isnull(properties.natGateway))
| project id = tolower(id), name, type = tolower(type), location, subscriptionId,
  sku = tostring(sku.name), size = tostring(properties.diskSizeGB), created = tostring(properties.timeCreated)`

type azureIdleRow struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Location       string `json:"location"`
	SubscriptionID string `json:"subscriptionId"`
	SKU            string `json:"sku"`
	Size           string `json:"size"`
	Created        string `json:"created"`
}

type azureAdvice struct {
	connection spec.CloudConnection
	options    Options
	token      string
	c          *adviceCollector
}

func collectAzureAdvice(ctx context.Context, connection spec.CloudConnection, options Options, c *adviceCollector) {
	token, err := azureToken(ctx, connection, options, options.HTTP)
	if err != nil {
		for _, section := range []string{spec.AdviceRightsizing, spec.AdviceChecks, spec.AdviceCommitments,
			spec.AdvicePlanner} {
			c.section(section, spec.AdviceError, "couldn't sign in to Microsoft Entra: "+err.Error())
		}
		return
	}
	a := &azureAdvice{connection: connection, options: options, token: token, c: c}
	t := newTracker()
	subscriptions := []string{connection.ScopeID}
	if connection.Scope == "management_group" {
		rows, ok := azureGraph[azureSubscriptionRow](ctx, connection, options, token, azureSubscriptionsQuery,
			maxAzureResources, t)
		if !ok {
			for _, section := range []string{spec.AdviceRightsizing, spec.AdvicePlanner} {
				c.section(section, spec.AdviceError, "couldn't list the management group's subscriptions")
			}
		}
		subscriptions = subscriptions[:0]
		for _, row := range rows {
			subscriptions = append(subscriptions, row.SubscriptionID)
		}
	}
	for _, subscription := range subscriptions {
		a.advisor(ctx, subscription)
		a.reservationOptions(ctx, subscription)
		a.savingsPlanOptions(ctx, subscription)
	}
	a.checks(ctx)
	a.commitments(ctx)
	for _, section := range []string{spec.AdviceRightsizing, spec.AdviceChecks, spec.AdviceCommitments,
		spec.AdvicePlanner} {
		c.section(section, spec.AdviceOK, "")
	}
}

// get reads every page of a list endpoint; false after a failure it has recorded.
func (a *azureAdvice) get(ctx context.Context, section, endpoint, permission string, each func(json.RawMessage)) bool {
	for pages := 0; endpoint != "" && pages < azureMaxPages; pages++ {
		var page struct {
			Value    []json.RawMessage `json:"value"`
			NextLink string            `json:"nextLink"`
		}
		status, err := azureJSON(ctx, a.options.HTTP, http.MethodGet, endpoint, a.token, nil, &page)
		switch {
		case status == http.StatusForbidden || status == http.StatusUnauthorized:
			a.c.section(section, spec.AdviceNeedsPermission, permission)
			return false
		case err != nil:
			a.c.section(section, spec.AdviceError, err.Error())
			return false
		}
		for _, item := range page.Value {
			each(item)
		}
		endpoint = page.NextLink
	}
	return true
}

func (a *azureAdvice) management() string {
	return orDefault(a.options.AzureManagement, azureManagement)
}

type azureAdvisorItem struct {
	ID         string `json:"id"`
	Properties struct {
		ImpactedField    string `json:"impactedField"`
		ImpactedValue    string `json:"impactedValue"`
		Impact           string `json:"impact"`
		ShortDescription struct {
			Problem  string `json:"problem"`
			Solution string `json:"solution"`
		} `json:"shortDescription"`
		ResourceMetadata struct {
			ResourceID string `json:"resourceId"`
		} `json:"resourceMetadata"`
		ExtendedProperties map[string]string `json:"extendedProperties"`
	} `json:"properties"`
}

// advisor reads Azure Advisor's cost recommendations; purchase ones are left to the planner.
func (a *azureAdvice) advisor(ctx context.Context, subscription string) {
	endpoint := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Advisor/recommendations?api-version=%s&$filter=%s",
		a.management(), url.PathEscape(subscription), azureAdvisorAPIVersion, url.QueryEscape("Category eq 'Cost'"))
	a.get(ctx, spec.AdviceRightsizing, endpoint,
		"Microsoft.Advisor/recommendations/read (Cost Management Reader or Reader)", func(raw json.RawMessage) {
			var item azureAdvisorItem
			if json.Unmarshal(raw, &item) == nil {
				if rec, ok := advisorRecommendation(item, subscription); ok {
					a.c.addRecs(rec)
				}
			}
		})
}

func advisorRecommendation(item azureAdvisorItem, subscription string) (spec.CostRecommendation, bool) {
	props := item.Properties
	extended := props.ExtendedProperties
	text := strings.ToLower(props.ShortDescription.Problem + " " + props.ShortDescription.Solution)
	for _, purchase := range []string{"reservedresourcetype", "commitmentamount", "term", "lookbackperiod"} {
		for key := range extended {
			if strings.EqualFold(key, purchase) {
				return spec.CostRecommendation{}, false
			}
		}
	}
	if strings.Contains(text, "reserved instance") || strings.Contains(text, "savings plan") ||
		strings.Contains(text, "reservation") {
		return spec.CostRecommendation{}, false
	}
	resource := strings.ToLower(props.ResourceMetadata.ResourceID)
	if resource == "" {
		return spec.CostRecommendation{}, false
	}
	current, target := attr(extended, "currentSku"), attr(extended, "targetSku")
	kind := spec.RecommendModernize
	switch {
	case strings.EqualFold(target, "shutdown"):
		kind = spec.RecommendIdle
	case target != "":
		kind = spec.RecommendRightsizing
	case containsAny(text, "unattached", "idle", "unused", "delete", "shut down", "shutdown", "stop "):
		kind = spec.RecommendIdle
	case containsAny(text, "storage", "blob", "lifecycle", "tier", "snapshot"):
		kind = spec.RecommendStorage
	}
	action := firstNonEmptyString(props.ShortDescription.Solution, props.ShortDescription.Problem)
	if kind == spec.RecommendRightsizing {
		action = fmt.Sprintf("Change %s to %s", firstNonEmptyString(current, "the current size"), target)
	}
	currency := strings.ToUpper(attr(extended, "savingsCurrency"))
	savings := int64(0)
	if annual, err := strconv.ParseFloat(attr(extended, "annualSavingsAmount"), 64); err == nil && annual > 0 {
		savings = microsOf(annual / 12)
	} else if monthly, err := strconv.ParseFloat(attr(extended, "savingsAmount"), 64); err == nil {
		savings = microsOf(monthly)
	}
	evidence := map[string]string{}
	for key, value := range extended {
		if len(evidence) < 12 && !containsAny(strings.ToLower(key), "savings", "currency") {
			evidence[key] = clip(value, 200)
		}
	}
	if props.ShortDescription.Problem != "" {
		evidence["problem"] = clip(props.ShortDescription.Problem, 300)
	}
	effort := "medium"
	if kind == spec.RecommendIdle {
		effort = "low"
	}
	risk := map[string]string{"High": "medium", "Medium": "low", "Low": "low"}[props.Impact]
	return spec.CostRecommendation{
		Key: "advisor:" + strings.ToLower(item.ID), Source: spec.RecommendFromAdvisor, Kind: kind,
		Account: subscription, Region: firstNonEmptyString(attr(extended, "region"), "global"),
		ResourceKey: resource, ResourceName: firstNonEmptyString(props.ImpactedValue, lastSegment(resource)),
		ResourceType: azureTypeName(props.ImpactedField), Action: clip(action, 300), Current: current,
		Target: target, Currency: currency, MonthlySavingsMicros: savings, Effort: effort,
		Risk: firstNonEmptyString(risk, "low"), Evidence: evidence,
	}, savings > 0
}

type azureAmount struct {
	Value    float64
	Currency string
}

// UnmarshalJSON reads a legacy plain number or a modern {currency, value} object.
func (a *azureAmount) UnmarshalJSON(data []byte) error {
	var number float64
	if json.Unmarshal(data, &number) == nil {
		a.Value = number
		return nil
	}
	var object struct {
		Value    float64 `json:"value"`
		Currency string  `json:"currency"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	a.Value, a.Currency = object.Value, object.Currency
	return nil
}

type azureReservationRec struct {
	Properties struct {
		LookBackPeriod                 json.RawMessage `json:"lookBackPeriod"`
		RecommendedQuantity            float64         `json:"recommendedQuantity"`
		Term                           string          `json:"term"`
		CostWithNoReservedInstances    azureAmount     `json:"costWithNoReservedInstances"`
		TotalCostWithReservedInstances azureAmount     `json:"totalCostWithReservedInstances"`
		NetSavings                     azureAmount     `json:"netSavings"`
		NormalizedSize                 string          `json:"normalizedSize"`
		SKUName                        string          `json:"skuName"`
		Location                       string          `json:"location"`
		ResourceType                   string          `json:"resourceType"`
		CurrencyCode                   string          `json:"currencyCode"`
	} `json:"properties"`
	Location string `json:"location"`
	SKU      string `json:"sku"`
}

// reservationOptions reads Azure's reservation recommendations per term and look-back. Azure
// charges the same whether paid monthly or upfront, so both payments get the same numbers.
func (a *azureAdvice) reservationOptions(ctx context.Context, subscription string) {
	for _, term := range azureTerms {
		for _, days := range spec.AdviceLookbacks {
			filter := fmt.Sprintf("properties/scope eq 'Single' AND properties/lookBackPeriod eq 'Last%dDays' "+
				"AND properties/term eq '%s'", days, term.api)
			endpoint := fmt.Sprintf(
				"%s/subscriptions/%s/providers/Microsoft.Consumption/reservationRecommendations?api-version=%s&$filter=%s",
				a.management(),
				url.PathEscape(subscription),
				azureReservationRecsVersion,
				url.QueryEscape(filter),
			)
			option := spec.PurchaseOption{Type: spec.CommitAzureReserve, Term: term.term, LookbackDays: days,
				Offered: true}
			var noReservations, withReservations, savings int64
			ok := a.get(ctx, spec.AdvicePlanner, endpoint, "Microsoft.Consumption/reservationRecommendations/read "+
				"(Cost Management Reader)", func(raw json.RawMessage) {
				var rec azureReservationRec
				if json.Unmarshal(raw, &rec) != nil {
					return
				}
				p := rec.Properties
				option.Currency = firstNonEmptyString(option.Currency, p.CostWithNoReservedInstances.Currency,
					p.CurrencyCode)
				noReservations += microsOf(p.CostWithNoReservedInstances.Value)
				withReservations += microsOf(p.TotalCostWithReservedInstances.Value)
				lineSavings := microsOf(p.NetSavings.Value)
				savings += lineSavings
				sku := firstNonEmptyString(p.SKUName, rec.SKU, p.NormalizedSize, "reservation")
				location := firstNonEmptyString(p.Location, rec.Location)
				if len(option.Lines) < 50 {
					option.Lines = append(option.Lines, spec.PurchaseLine{
						Description: fmt.Sprintf(
							"%s × %s",
							strconv.FormatFloat(p.RecommendedQuantity, 'f', -1, 64),
							sku,
						),
						Account:              subscription,
						Region:               location,
						Quantity:             strconv.FormatFloat(p.RecommendedQuantity, 'f', -1, 64),
						MonthlySavingsMicros: monthlyFrom(lineSavings, days),
					})
				}
			})
			if !ok {
				return
			}
			// The amounts cover the look-back period.
			option.MonthlySavingsMicros = monthlyFrom(savings, days)
			option.OnDemandMonthlyMicros = monthlyFrom(noReservations, days)
			option.MonthlyRecurringMicros = monthlyFrom(withReservations, days)
			if noReservations > 0 {
				option.SavingsPercent = float64(savings) * 100 / float64(noReservations)
			}
			switch {
			case savings <= 0:
				option.Reason = "No purchase recommended for this usage"
				option.Lines = nil
			case len(option.Lines) == 1:
				option.Commitment = option.Lines[0].Description + " in " + option.Lines[0].Region
			default:
				option.Commitment = fmt.Sprintf("%d reservations", len(option.Lines))
			}
			a.addBothPayments(option)
		}
	}
}

type azureBenefitRec struct {
	Properties struct {
		CurrencyCode          string  `json:"currencyCode"`
		CostWithoutBenefit    float64 `json:"costWithoutBenefit"`
		CommitmentGranularity string  `json:"commitmentGranularity"`
		RecommendationDetails struct {
			CommitmentAmount             float64 `json:"commitmentAmount"`
			SavingsAmount                float64 `json:"savingsAmount"`
			SavingsPercentage            float64 `json:"savingsPercentage"`
			TotalCost                    float64 `json:"totalCost"`
			BenefitCost                  float64 `json:"benefitCost"`
			CoveragePercentage           float64 `json:"coveragePercentage"`
			AverageUtilizationPercentage float64 `json:"averageUtilizationPercentage"`
		} `json:"recommendationDetails"`
	} `json:"properties"`
}

// savingsPlanOptions reads Azure's savings plan recommendations per term and look-back.
func (a *azureAdvice) savingsPlanOptions(ctx context.Context, subscription string) {
	for _, term := range azureTerms {
		for _, days := range spec.AdviceLookbacks {
			filter := fmt.Sprintf(
				"properties/lookBackPeriod eq 'Last%dDays' AND properties/term eq '%s'",
				days,
				term.api,
			)
			endpoint := fmt.Sprintf(
				"%s/subscriptions/%s/providers/Microsoft.CostManagement/benefitRecommendations?api-version=%s&$filter=%s",
				a.management(),
				url.PathEscape(subscription),
				azureBenefitRecsVersion,
				url.QueryEscape(filter),
			)
			option := spec.PurchaseOption{Type: spec.CommitAzureSP, Term: term.term, LookbackDays: days, Offered: true}
			ok := a.get(ctx, spec.AdvicePlanner, endpoint, "Microsoft.CostManagement/benefitRecommendations/read "+
				"(Cost Management Reader)", func(raw json.RawMessage) {
				var rec azureBenefitRec
				if json.Unmarshal(raw, &rec) != nil || option.HourlyCommitmentMicros > 0 {
					return
				}
				p, details := rec.Properties, rec.Properties.RecommendationDetails
				option.Currency = p.CurrencyCode
				option.HourlyCommitmentMicros = microsOf(details.CommitmentAmount)
				// Amounts cover the look-back period.
				option.MonthlySavingsMicros = monthlyFrom(microsOf(details.SavingsAmount), days)
				option.OnDemandMonthlyMicros = monthlyFrom(microsOf(p.CostWithoutBenefit), days)
				option.MonthlyRecurringMicros = option.HourlyCommitmentMicros * hoursPerMonth
				option.SavingsPercent = details.SavingsPercentage
				option.CoveragePercent = details.CoveragePercentage
				option.AverageUtilization = details.AverageUtilizationPercentage
			})
			if !ok {
				return
			}
			if option.HourlyCommitmentMicros <= 0 || option.MonthlySavingsMicros <= 0 {
				option.Reason = "No purchase recommended for this usage"
			} else {
				option.Commitment = decimalText(option.HourlyCommitmentMicros) + " an hour"
			}
			a.addBothPayments(option)
		}
	}
}

func (a *azureAdvice) addBothPayments(option spec.PurchaseOption) {
	for _, payment := range []string{spec.PayMonthly, spec.PayUpfront} {
		copied := option
		copied.Payment = payment
		copied.Lines = slices.Clone(option.Lines)
		if payment == spec.PayUpfront && option.MonthlyRecurringMicros > 0 {
			termMonths := int64(12)
			if option.Term == spec.Term3Years {
				termMonths = 36
			}
			copied.UpfrontMicros = option.MonthlyRecurringMicros * termMonths
			copied.MonthlyRecurringMicros = 0
		}
		a.c.addOption(copied)
	}
}

// checks finds unattached disks and unused public IPs. Resource Graph answers only for what
// the identity can read, so without Reader an empty answer would look like "nothing idle".
func (a *azureAdvice) checks(ctx context.Context) {
	if !slices.Contains(a.connection.Uses, spec.UseInventory) && !slices.Contains(a.connection.Uses, spec.UsePosture) {
		a.c.section(spec.AdviceChecks, spec.AdviceNotEnabled,
			"Obzly's idle checks need Reader: add Inventory or Posture to the connection")
		return
	}
	t := newTracker()
	rows, ok := azureGraph[azureIdleRow](ctx, a.connection, a.options, a.token, azureIdleQuery, maxAzureResources, t)
	if !ok {
		state, reason := spec.AdviceError, "the idle resources query failed"
		if result := t.result(); result.State == spec.UseNeedsPermission {
			state, reason = spec.AdviceNeedsPermission, "Reader role"
		} else if errs := t.errorList(); len(errs) > 0 {
			reason = errs[0]
		}
		a.c.section(spec.AdviceChecks, state, reason)
		return
	}
	for _, row := range rows {
		rec := spec.CostRecommendation{Source: spec.RecommendFromObzly, Kind: spec.RecommendIdle,
			Account: row.SubscriptionID, Region: firstNonEmptyString(row.Location, "global"), ResourceKey: row.ID,
			ResourceName: row.Name, SavingsShare: 1, Effort: "low", Risk: "low",
			Evidence: map[string]string{"sku": row.SKU, "resourceGroup": resourceGroupOf(row.ID)}}
		if row.Type == "microsoft.compute/disks" {
			rec.Key, rec.ResourceType = "obzly:disk-unattached:"+row.ID, "azure_managed_disk"
			rec.Action = "Delete the unattached managed disk (snapshot it first if you need the data)"
			rec.Evidence["size_gib"], rec.Evidence["created"] = row.Size, row.Created
		} else {
			rec.Key, rec.ResourceType = "obzly:public-ip-unused:"+row.ID, "azure_public_ip"
			rec.Action = "Delete the unused public IP address"
		}
		a.c.addRecs(rec)
	}
}

type azureUtilization struct {
	Aggregates []struct {
		Grain     float64 `json:"grain"`
		GrainUnit string  `json:"grainUnit"`
		Value     float64 `json:"value"`
	} `json:"aggregates"`
}

func (u azureUtilization) thirtyDays() *float64 {
	for _, aggregate := range u.Aggregates {
		if aggregate.Grain == 30 && strings.EqualFold(aggregate.GrainUnit, "days") {
			value := aggregate.Value
			return &value
		}
	}
	return nil
}

// commitments lists the reservations and savings plans the identity can see. Both need a
// tenant-level reader role (Reservations Reader, Savings plan Reader) the templates can't assign.
func (a *azureAdvice) commitments(ctx context.Context) {
	reservations := a.management() + "/providers/Microsoft.Capacity/reservations?api-version=" + azureCapacityAPIVersion
	ok := a.get(ctx, spec.AdviceCommitments, reservations,
		"Reservations Reader (assigned at /providers/Microsoft.Capacity)", func(raw json.RawMessage) {
			var item struct {
				ID  string `json:"id"`
				SKU struct {
					Name string `json:"name"`
				} `json:"sku"`
				Location   string `json:"location"`
				Properties struct {
					DisplayName       string           `json:"displayName"`
					SKUDescription    string           `json:"skuDescription"`
					Quantity          float64          `json:"quantity"`
					Term              string           `json:"term"`
					BillingPlan       string           `json:"billingPlan"`
					ProvisioningState string           `json:"provisioningState"`
					EffectiveDateTime string           `json:"effectiveDateTime"`
					ExpiryDateTime    string           `json:"expiryDateTime"`
					ExpiryDate        string           `json:"expiryDate"`
					Utilization       azureUtilization `json:"utilization"`
				} `json:"properties"`
			}
			if json.Unmarshal(raw, &item) != nil || !strings.EqualFold(item.Properties.ProvisioningState, "Succeeded") {
				return
			}
			p := item.Properties
			description := fmt.Sprintf("%s: %s × %s", firstNonEmptyString(p.DisplayName, "Reservation"),
				strconv.FormatFloat(p.Quantity, 'f', -1, 64), firstNonEmptyString(p.SKUDescription, item.SKU.Name))
			a.c.addCommitments(spec.Commitment{
				ID: strings.ToLower(item.ID), Type: spec.CommitAzureReserve, Region: item.Location,
				Description: description, Term: azureTerm(p.Term), Payment: strings.ToLower(p.BillingPlan),
				Start: ceTime(p.EffectiveDateTime), End: ceTime(firstNonEmptyString(p.ExpiryDateTime, p.ExpiryDate)),
				UtilizationPercent: p.Utilization.thirtyDays(),
			})
		})
	if !ok {
		return
	}
	plans := a.management() + "/providers/Microsoft.BillingBenefits/savingsPlans?api-version=" + azureBenefitsAPIVersion
	a.get(ctx, spec.AdviceCommitments, plans, "Savings plan Reader (assigned on the savings plans)",
		func(raw json.RawMessage) {
			var item struct {
				ID         string `json:"id"`
				Properties struct {
					DisplayName string `json:"displayName"`
					Commitment  struct {
						Grain        string  `json:"grain"`
						CurrencyCode string  `json:"currencyCode"`
						Amount       float64 `json:"amount"`
					} `json:"commitment"`
					Term              string           `json:"term"`
					BillingPlan       string           `json:"billingPlan"`
					ProvisioningState string           `json:"provisioningState"`
					EffectiveDateTime string           `json:"effectiveDateTime"`
					ExpiryDateTime    string           `json:"expiryDateTime"`
					Utilization       azureUtilization `json:"utilization"`
				} `json:"properties"`
			}
			if json.Unmarshal(raw, &item) != nil || !strings.EqualFold(item.Properties.ProvisioningState, "Succeeded") {
				return
			}
			p := item.Properties
			hourly := microsOf(p.Commitment.Amount)
			monthly := hourly * hoursPerMonth
			commitment := spec.Commitment{
				ID: strings.ToLower(item.ID), Type: spec.CommitAzureSP,
				Description: fmt.Sprintf("%s: %s an hour", firstNonEmptyString(p.DisplayName, "Savings plan"),
					decimalText(hourly)),
				Term: azureTerm(p.Term), Payment: strings.ToLower(p.BillingPlan), Start: ceTime(p.EffectiveDateTime),
				End: ceTime(p.ExpiryDateTime), Currency: p.Commitment.CurrencyCode, MonthlyCostMicros: monthly,
				UtilizationPercent: p.Utilization.thirtyDays(),
			}
			if used := commitment.UtilizationPercent; used != nil {
				commitment.UnusedMonthlyMicros = int64(float64(monthly) * max(0, 100-*used) / 100)
			}
			a.c.addCommitments(commitment)
		})
}

func azureTerm(value string) string {
	switch strings.ToUpper(value) {
	case "P1Y":
		return spec.Term1Year
	case "P3Y":
		return spec.Term3Years
	}
	return ""
}

func azureTypeName(field string) string {
	field = strings.ToLower(field)
	switch field {
	case "microsoft.compute/virtualmachines":
		return "azure_virtual_machine"
	case "microsoft.compute/disks":
		return "azure_managed_disk"
	case "microsoft.sql/servers/databases":
		return "azure_sql_database"
	}
	if i := strings.LastIndex(field, "/"); i >= 0 {
		return "azure_" + field[i+1:]
	}
	return field
}

func containsAny(text string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}
