import logging
import smtplib
from email.mime.text import MIMEText

LOGGER = logging.getLogger(__name__)


def sms_notification(
    msg_text: str,
    recipients: list,
    smtp_user: str,
    smtp_password: str,
    smtp_host: str,
    smtp_port: int,
) -> None:
    LOGGER.debug(f"Sending message: {msg_text}\n To Recipients: {recipients}")
    msg = MIMEText(msg_text)
    msg["From"] = smtp_user

    # Create server object with SSL option
    server = smtplib.SMTP_SSL(smtp_host, smtp_port)

    # Perform operations via server
    server.login(smtp_user, smtp_password)
    server.sendmail(smtp_user, recipients, msg.as_string())
    server.quit()


def create_msg_text(products):
    LOGGER.debug(f"Creating message with: {products}")
    message = "Products found:\n"
    for product in products:
        message += f"{product.name}\n{product.url}\n"
    return message
