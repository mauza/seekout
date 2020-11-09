import smtplib
from email.mime.text import MIMEText


def sms_notification(
    msg_text: str,
    recipients: list,
    smtp_user: str,
    smtp_password: str,
    smtp_host: str,
    smtp_port: int,
) -> None:
    msg = MIMEText(msg_text)
    msg["From"] = smtp_user

    # Create server object with SSL option
    server = smtplib.SMTP_SSL(smtp_host, smtp_port)

    # Perform operations via server
    server.login(smtp_user, smtp_password)
    server.sendmail(smtp_user, recipients, msg.as_string())
    server.quit()