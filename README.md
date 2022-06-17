bulkremoval
===========

This is a tool for bulk removal of spam messages from the [Ergo IRC server](https://github.com/ergochat/ergo).

You must have the operator privileges `nofakelag`, `sajoin`, and `history`. Configure the tool like this:

```bash
export IRCEVENT_SERVER=localhost:6667
export IRCEVENT_CHANNEL="#test"
# optional, log the bot into an account with SASL
export IRCEVENT_SASL_LOGIN=""
export IRCEVENT_SASL_PASSWORD=""
# comment this out for TLS (do not use plaintext over the public Internet)
export IRCEVENT_USE_PLAINTEXT="true"
# OPER name and password:
export IRCEVENT_OPER_NAME="root"
export IRCEVENT_OPER_PASSWORD="shivarampassphrase"
# NUH glob to match (this is currently case-sensitive)
export IRCEVENT_GLOB="netcat!*@*"
# time at which to start examining messages, in YYYY-MM-DDThh:mm:ss.sssZ format:
export IRCEVENT_START_TIME="2022-05-03T22:09:02.160Z"
```
