from seekout.alerts.sms import sms_notification


def test_sms():
    try:
        sms_notification(
            msg_text="Hello",
            recipients=["mauza@mauza.net"],
            smtp_user="doesntwork",
            smtp_password="password",
            smtp_host="localhost",
            smtp_port=576,
        )
    except ConnectionRefusedError:
        pass
