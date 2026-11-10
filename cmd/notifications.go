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
