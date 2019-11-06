package signalfx

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	alertmuting "github.com/signalfx/signalfx-go/alertmuting"
)

func alertMutingRuleResource() *schema.Resource {
	return &schema.Resource{
		Schema: map[string]*schema.Schema{
			"description": &schema.Schema{
				Type:        schema.TypeString,
				Required:    true,
				Description: "Description of the rule.",
			},
			"detectors": &schema.Schema{
				Type:        schema.TypeList,
				Optional:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Detectors to which this muting rule applies.",
			},
			"filter": &schema.Schema{
				Type:        schema.TypeSet,
				Required:    true,
				Description: "List of alert muting filters for this rule.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"property": &schema.Schema{
							Type:        schema.TypeString,
							Required:    true,
							Description: "The property to filter by.",
						},
						"property_value": &schema.Schema{
							Type:        schema.TypeString,
							Required:    true,
							Description: "The value of the property to filter by.",
						},
						"negated": &schema.Schema{
							Type:        schema.TypeBool,
							Optional:    true,
							Default:     false,
							Description: "(false by default) Whether this filter should be a \"not\" filter",
						},
					},
				},
			},
			"start_time": &schema.Schema{
				Type:        schema.TypeInt,
				Required:    true,
				Description: "Starting time of an alert muting rule as a Unix timestamp, in seconds.",
				ForceNew:    true,
			},
			"stop_time": &schema.Schema{
				Type:        schema.TypeInt,
				Optional:    true,
				Default:     0,
				Description: "Stop time of an alert muting rule as a Unix timestamp, in seconds.",
			},
			"effective_start_time": &schema.Schema{
				Type:     schema.TypeInt,
				Computed: true,
			},
		},
		Create: alertMutingRuleCreate,
		Read:   alertMutingRuleRead,
		Update: alertMutingRuleUpdate,
		Delete: alertMutingRuleDelete,
		Exists: alertMutingRuleExists,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},
		// CustomizeDiff: customdiff.All(
		// 	customdiff.ForceNewIfChange("start_time", func(old, new, meta interface{}) bool {
		// 		// If the old time was AFTER now then we can allow an
		// 		// update without forcing a new resource
		// 		log.Printf("[DEBUG] SignalFx: OLD %d", old.(int))
		// 		log.Printf("[DEBUG] SignalFx: NOW %d", time.Now().Unix())
		// 		if int64(old.(int)) >= time.Now().Unix() {
		// 			return false
		// 		} else {
		// 			// Oops, the mute rule has already begun which means
		// 			// we need to recreate the resource
		// 			log.Printf("[DEBUG] SignalFx: Flagging that a new mute rule is required because this start time has already passed.")
		// 			return true
		// 		}
		// 	}),
		// ),
	}
}

func getPayloadAlertMutingRule(d *schema.ResourceData) (*alertmuting.CreateUpdateAlertMutingRuleRequest, error) {

	tfFilters := d.Get("filter").(*schema.Set).List()

	var filterList []*alertmuting.AlertMutingRuleFilter
	for _, tfFilter := range tfFilters {
		tfFilter := tfFilter.(map[string]interface{})
		filter := &alertmuting.AlertMutingRuleFilter{
			Property:      tfFilter["property"].(string),
			PropertyValue: tfFilter["property_value"].(string),
			NOT:           tfFilter["negated"].(bool),
		}
		filterList = append(filterList, filter)
	}

	// Detectors is a convenience property that allows
	// the user a way to specific the detectors to which
	// this rule will apply without having to know the details
	// of how that happens.
	if val, ok := d.GetOk("detectors"); ok {
		for _, d := range val.([]interface{}) {
			filterList = append(filterList, &alertmuting.AlertMutingRuleFilter{
				Property:      "sf_detectorId",
				PropertyValue: d.(string),
				NOT:           false,
			})
		}
	}

	cuamrr := &alertmuting.CreateUpdateAlertMutingRuleRequest{
		Description: d.Get("description").(string),
		Filters:     filterList,
		StartTime:   int64(d.Get("start_time").(int) * 1000),
		StopTime:    int64(d.Get("stop_time").(int) * 1000),
	}

	return cuamrr, nil
}

func alertMutingRuleCreate(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*signalfxConfig)
	payload, err := getPayloadAlertMutingRule(d)
	if err != nil {
		return fmt.Errorf("Failed creating json payload: %s", err.Error())
	}

	debugOutput, _ := json.Marshal(payload)
	log.Printf("[DEBUG] SignalFx: Create Alert Muting Rule Payload: %s", string(debugOutput))

	amr, err := config.Client.CreateAlertMutingRule(payload)
	if err != nil {
		return err
	}
	d.SetId(amr.Id)

	return alertMutingRuleAPIToTF(d, amr)
}

func alertMutingRuleExists(d *schema.ResourceData, meta interface{}) (bool, error) {
	config := meta.(*signalfxConfig)
	_, err := config.Client.GetAlertMutingRule(d.Id())
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func alertMutingRuleRead(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*signalfxConfig)
	amr, err := config.Client.GetAlertMutingRule(d.Id())
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			d.SetId("")
		}
		return err
	}

	return alertMutingRuleAPIToTF(d, amr)
}

func alertMutingRuleAPIToTF(d *schema.ResourceData, amr *alertmuting.AlertMutingRule) error {
	debugOutput, _ := json.Marshal(amr)
	log.Printf("[DEBUG] SignalFx: Got Alert Muting Rule to enState: %s", string(debugOutput))

	if err := d.Set("description", amr.Description); err != nil {
		return err
	}

	if amr.Filters != nil && len(amr.Filters) > 0 {
		var filters []map[string]interface{}
		var detectors []string
		for _, f := range amr.Filters {

			switch f.Property {
			// The API does not differentiate, but we do to make things
			// easier for the user, so separate detectors out into their
			// own propery.
			case "sf_detectorId":
				detectors = append(detectors, f.PropertyValue)
			default:
				filters = append(filters, map[string]interface{}{
					"property":       f.Property,
					"property_value": f.PropertyValue,
					"negated":        f.NOT,
				})
			}
		}
		if err := d.Set("filter", filters); err != nil {
			return err
		}
		if err := d.Set("detectors", detectors); err != nil {
			return err
		}
		// The API changes `startTime` to be >= the current
		// timestamp at the time of the API call. This means
		// it will pretty much never agree with what the user specified.
		// To accomodate this we will store the "effective" start time
		// as a computed attribute, then…
		if err := d.Set("effective_start_time", amr.StartTime); err != nil {
			return err
		}
		// We will ignore the start time because it doesn't matter.
		// See above.
		if err := d.Set("stop_time", amr.StopTime/1000); err != nil {
			return err
		}
	}

	return nil
}

func alertMutingRuleUpdate(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*signalfxConfig)
	payload, err := getPayloadAlertMutingRule(d)
	if err != nil {
		return fmt.Errorf("Failed creating json payload: %s", err.Error())
	}

	// If we have an effective start time…
	if val, ok := d.GetOk("effective_start_time"); ok {
		est := val.(int)
		st := d.Get("start_time").(int)
		// and if the start time is in the past…
		if int64(st) <= time.Now().Unix() {
			// then replace the start time with the effective start
			// time. This papers over the fact that the API basically
			// ignores our start times unless they are in the future.
			payload.StartTime = int64(est)
			log.Printf("[DEBUG] SignalFx: Replaced start time with effective time")
		} else {
			log.Printf("[DEBUG] SignalFx: Using specified start time")
			payload.StartTime = int64(d.Get("start_time").(int)) * 1000
		}
	}

	debugOutput, _ := json.Marshal(payload)
	log.Printf("[DEBUG] SignalFx: Update Alert Muting Rule Payload: %s", string(debugOutput))

	det, err := config.Client.UpdateAlertMutingRule(d.Id(), payload)
	if err != nil {
		return err
	}
	log.Printf("[DEBUG] SignalFx: Update Alert Muting Rule Response: %v", det)

	d.SetId(det.Id)
	return alertMutingRuleAPIToTF(d, det)
}

func alertMutingRuleDelete(d *schema.ResourceData, meta interface{}) error {
	config := meta.(*signalfxConfig)

	return config.Client.DeleteAlertMutingRule(d.Id())
}
