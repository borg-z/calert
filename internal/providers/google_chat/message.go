package google_chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	alertmgrtmpl "github.com/prometheus/alertmanager/template"
)

const (
	maxMsgSize = 4096
)

var excludedLabels = map[string]bool{
	"alertname": true,
	"severity":  true,
}

func prepareGrafanaUrl(alert alertmgrtmpl.Alert) (string, error) {

	grafanaURL, ok := alert.Annotations["grafanaURL"]
	if !ok {
		return "", nil
	}

	grafanaDS, ok := alert.Annotations["grafanaDS"]
	if !ok {
		return "", nil
	}

	labels := alert.Labels

	var labelParts []string
	for k, v := range labels {
		if _, excluded := excludedLabels[k]; excluded {
			continue
		}
		part := fmt.Sprintf(`%s=\"%s\"`, k, v)
		labelParts = append(labelParts, part)
	}

	labelsString := "{" + strings.Join(labelParts, ",") + "}"
	escapedLabels := url.QueryEscape(labelsString)
	q := fmt.Sprintf("%s  |~ `(?i)error`", escapedLabels)
	now := time.Now()

	fiveMinutes := int64(5 * 60 * 1000)
	from := now.Add(-time.Duration(fiveMinutes)*time.Millisecond).UnixNano() / 1e6
	to := now.Add(time.Duration(fiveMinutes)*time.Millisecond).UnixNano() / 1e6

	panesValue := fmt.Sprintf(`{"xph":{"datasource":"%s","queries":[{"refId":"A","expr":"%s","queryType":"range","datasource":{"type":"loki","uid":"%s"},"editorMode":"code"}],"range":{"from":"%d","to":"%d"}}}`, grafanaDS, q, grafanaDS, from, to)
	finalURL := fmt.Sprintf("%s/explore?schemaVersion=1&panes=%s&orgId=1", grafanaURL, panesValue)
	encodedFinalURL := strings.ReplaceAll(finalURL, "\"", "%22")
	return encodedFinalURL, nil
}

func (m *GoogleChatManager) prepareMessage(alert alertmgrtmpl.Alert) ([]ChatMessage, error) {

	grafanaUrl, _ := prepareGrafanaUrl(alert)

	msg := ChatMessage{
		CardsV2: []CardV2{
			{
				CardId: "unique-card-id",
				Card: CardDetail{
					Header: Header{
						Title:    "🔥 " + alert.Annotations["title"],
						Subtitle: alert.Annotations["description"],
						ImageUrl: "https://grafana.com/static/assets/img/fav32.png",
					},
					Sections: []Section{
						{
							Header:                    "Summary",
							Collapsible:               true,
							UncollapsibleWidgetsCount: 1,
							Widgets: func() []Widget {
								var widgets []Widget
								if len(grafanaUrl) > 0 {
									grafanaButton := Widget{
										ButtonList: &ButtonList{
											Buttons: []Button{
												{
													Text: "Grafana",
													OnClick: OnClick{
														OpenLink: OpenLink{
															URL: grafanaUrl,
														},
													},
												},
											},
										},
									}
									widgets = append(widgets, grafanaButton)
								}
								for key, value := range alert.Labels {
									widget := Widget{
										DecoratedText: &DecoratedText{
											StartIcon: StartIcon{KnownIcon: "DESCRIPTION"},
											Text:      fmt.Sprintf("%s: %s", key, value),
										},
									}
									widgets = append(widgets, widget)
								}
								return widgets
							}(),
						},
					},
				},
			},
		},
	}

	messages := make([]ChatMessage, 0)
	messages = append(messages, msg)

	return messages, nil
}

// sendMessage pushes out a notification to Google Chat space.
func (m *GoogleChatManager) sendMessage(msg ChatMessage, threadKey string) error {
	out, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	fmt.Println(string(out))

	// Parse the webhook URL to add `?threadKey` param.
	u, err := url.Parse(m.endpoint)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("threadKey", threadKey)
	u.RawQuery = q.Encode()
	endpoint := u.String()

	// Prepare the request.
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(out))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Send the request.
	m.lo.WithField("url", endpoint).WithField("msg", msg).Debug("sending alert")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		m.lo.WithField("status", resp.StatusCode).Error("Non OK HTTP Response received from Google Chat Webhook endpoint")

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return errors.New("error reading response from gchat")
		}

		bodyString := string(bodyBytes)
		fmt.Println(bodyString)

		return errors.New("non ok response from gchat")
	}

	return nil
}
