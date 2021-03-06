package notifiers

import (
	"bytes"
	"io"
	"mime/multipart"
	"os"
	"strconv"
	"strings"

	"github.com/grafana/grafana/pkg/bus"
	"github.com/grafana/grafana/pkg/components/simplejson"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/services/alerting"
	"github.com/grafana/grafana/pkg/setting"
)

func init() {
	alerting.RegisterNotifier(&alerting.NotifierPlugin{
		Type:        "discord",
		Name:        "Discord",
		Description: "Sends notifications to Discord",
		Factory:     NewDiscordNotifier,
		OptionsTemplate: `
      <h3 class="page-heading">Discord settings</h3>
      <div class="gf-form max-width-30">
        <span class="gf-form-label width-10">Message Content</span>
        <input type="text"
          class="gf-form-input max-width-30"
          ng-model="ctrl.model.settings.content"
          data-placement="right">
        </input>
        <info-popover mode="right-absolute">
          Mention a group using @ or a user using <@ID> when notifying in a channel
        </info-popover>
      </div>
      <div class="gf-form  max-width-30">
        <span class="gf-form-label width-10">Webhook URL</span>
        <input type="text" required class="gf-form-input max-width-30" ng-model="ctrl.model.settings.url" placeholder="Discord webhook URL"></input>
      </div>
    `,
	})
}

func NewDiscordNotifier(model *models.AlertNotification) (alerting.Notifier, error) {
	content := model.Settings.Get("content").MustString()
	url := model.Settings.Get("url").MustString()
	if url == "" {
		return nil, alerting.ValidationError{Reason: "Could not find webhook url property in settings"}
	}

	return &DiscordNotifier{
		NotifierBase: NewNotifierBase(model),
		Content:      content,
		WebhookURL:   url,
		log:          log.New("alerting.notifier.discord"),
	}, nil
}

type DiscordNotifier struct {
	NotifierBase
	Content    string
	WebhookURL string
	log        log.Logger
}

func (this *DiscordNotifier) Notify(evalContext *alerting.EvalContext) error {
	this.log.Info("Sending alert notification to", "webhook_url", this.WebhookURL)

	ruleUrl, err := evalContext.GetRuleUrl()
	if err != nil {
		this.log.Error("Failed get rule link", "error", err)
		return err
	}

	bodyJSON := simplejson.New()
	bodyJSON.Set("username", "Grafana")

	if this.Content != "" {
		bodyJSON.Set("content", this.Content)
	}

	fields := make([]map[string]interface{}, 0)

	for _, evt := range evalContext.EvalMatches {

		fields = append(fields, map[string]interface{}{
			"name":   evt.Metric,
			"value":  evt.Value.FullString(),
			"inline": true,
		})
	}

	footer := map[string]interface{}{
		"text":     "Grafana v" + setting.BuildVersion,
		"icon_url": "https://grafana.com/assets/img/fav32.png",
	}

	color, _ := strconv.ParseInt(strings.TrimLeft(evalContext.GetStateModel().Color, "#"), 16, 0)

	embed := simplejson.New()
	embed.Set("title", evalContext.GetNotificationTitle())
	//Discord takes integer for color
	embed.Set("color", color)
	embed.Set("url", ruleUrl)
	embed.Set("description", evalContext.Rule.Message)
	embed.Set("type", "rich")
	embed.Set("fields", fields)
	embed.Set("footer", footer)

	var image map[string]interface{}
	var embeddedImage = false

	if evalContext.ImagePublicUrl != "" {
		image = map[string]interface{}{
			"url": evalContext.ImagePublicUrl,
		}
		embed.Set("image", image)
	} else {
		image = map[string]interface{}{
			"url": "attachment://graph.png",
		}
		embed.Set("image", image)
		embeddedImage = true
	}

	bodyJSON.Set("embeds", []interface{}{embed})

	json, _ := bodyJSON.MarshalJSON()

	cmd := &models.SendWebhookSync{
		Url:         this.WebhookURL,
		HttpMethod:  "POST",
		ContentType: "application/json",
	}

	if !embeddedImage {
		cmd.Body = string(json)
	} else {
		err := this.embedImage(cmd, evalContext.ImageOnDiskPath, json)
		if err != nil {
			this.log.Error("failed to embed image", "error", err)
			return err
		}
	}

	if err := bus.DispatchCtx(evalContext.Ctx, cmd); err != nil {
		this.log.Error("Failed to send notification to Discord", "error", err)
		return err
	}

	return nil
}

func (this *DiscordNotifier) embedImage(cmd *models.SendWebhookSync, imagePath string, existingJSONBody []byte) error {
	f, err := os.Open(imagePath)
	defer f.Close()
	if err != nil {
		if os.IsNotExist(err) {
			cmd.Body = string(existingJSONBody)
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
	}

	var b bytes.Buffer
	w := multipart.NewWriter(&b)

	fw, err := w.CreateFormField("payload_json")
	if err != nil {
		return err
	}

	if _, err = fw.Write([]byte(string(existingJSONBody))); err != nil {
		return err
	}

	fw, err = w.CreateFormFile("file", "graph.png")
	if err != nil {
		return err
	}

	if _, err = io.Copy(fw, f); err != nil {
		return err
	}

	w.Close()

	cmd.Body = b.String()
	cmd.ContentType = w.FormDataContentType()

	return nil
}
