# TODO
## user pesepctive
- [Done] fix the larkbot message resent problem
- support process image
- support process plan replayer(unzip replayer and start diagnose automatically)
- support diagnose by clinic link
- error handling: make sure all errors should return to user clearly(like network issue, rate limte ect)
- output a markdown file if output too long

## implementation pespective
- rotate log to avoid getting log too large
- optimize the write process of session store
- security related: better put agent in docker
- rate-limit
- use codex cli sdk to avoid start a new process for every user requesT
- store dedup map in disk to avoid the map was lost when process restart.
