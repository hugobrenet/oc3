package cmd

import "github.com/spf13/viper"

// setDefaultNotificationsConfig sets the defaults of the alert notifications,
// sent by the scheduler (task notify):
//
//	notifications:
//	  enable: true
//	  title: collector            # in the subjects
//	  public_url: https://collector.example.com   # the links of the messages
//	  max_alerts_per_message: 20  # detailed, the others counted
//	  max_attempts: 4
//	  workers: 4
//	  http_timeout: 10s          # the webhooks of the channels
//	  mail:                       # server.mail when not set
//	    server: smtp.example.com:587
//	    sender: collector@example.com
//	    security: auto            # auto, starttls, tls or none
//	    username: collector
//	    # password: from OC3_NOTIFICATIONS_MAIL_PASSWORD
//	    timeout: 30s
func setDefaultNotificationsConfig() {
	viper.SetDefault("notifications.enable", false)
	viper.SetDefault("notifications.title", "collector")
	viper.SetDefault("notifications.public_url", "")
	viper.SetDefault("notifications.max_alerts_per_message", 20)
	viper.SetDefault("notifications.max_attempts", 4)
	viper.SetDefault("notifications.workers", 4)
	viper.SetDefault("notifications.http_timeout", "10s")
	viper.SetDefault("notifications.mail.security", "auto")
	viper.SetDefault("notifications.mail.timeout", "30s")
}

// setDefaultAISchedulesConfig sets the defaults of the schedules of the AI
// assistant skills, run by the scheduler (tasks ai_schedules_queue and
// ai_schedules_run) on the unattended route of the agent, as a technical
// account of the OpenID provider, and sent to the notification channels
// (notifications.mail for the email channels, safe.keys_file for the secrets
// of the others):
//
//	ai_schedules:
//	  enable: true
//	  run_timeout: 10m            # a run, the tools of the assistant included
//	  workers: 2                  # the runs at once: at most the runs the agent
//	                              # allows one user (OPENSVC_AI_MAX_CONCURRENT_ASKS_PER_USER,
//	                              # 2 by default), all of them as the technical account
//	  agent:
//	    base_url: https://agent.example.com:8090
//	    path: /v1/oc3             # POST <path>/runs: read-only tools, nothing stored
//	    ca_file: /etc/oc3/agent-ca.pem
//	  collector:
//	    url: http://127.0.0.1:8081/api   # the API of the collector, https or loopback:
//	                              # before each run, the skill is read with the token
//	  auth:                       # a client_credentials grant of the collector's client
//	    token_url: https://sso.example.com/application/o/token/
//	    client_id: <the client id of the collector>
//	    username: oc3automation   # the reports tell what it reads: choose its groups;
//	                              # with no privilege, it changes nothing whatever
//	                              # the tools of the agent
//	    # password: from OC3_AI_SCHEDULES_AUTH_PASSWORD, or password_file
//	    scope: openid profile email
//
// A run that fails is told to the channels of its schedule when it asks for
// it (send_failures), once per cause: a configuration the scheduler cannot
// read fails every queued run with the same cause, told once per schedule.
//
// The migrations go first (oc3 db migrate), then the collector, the
// scheduler with the configuration.
func setDefaultAISchedulesConfig() {
	viper.SetDefault("ai_schedules.enable", false)
	viper.SetDefault("ai_schedules.run_timeout", "10m")
	viper.SetDefault("ai_schedules.workers", 2)
	viper.SetDefault("ai_schedules.collector.url", "http://127.0.0.1:8081/api")
	viper.SetDefault("ai_schedules.auth.scope", "openid profile email")
}
