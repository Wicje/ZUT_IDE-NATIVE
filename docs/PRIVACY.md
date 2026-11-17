# Privacy

What Zut and usemayoi.com send, and where. Nothing else leaves your machine unless you ask for it, as when you sign
in, push to a remote or open an SSH folder.

## Usage counts

Zut counts how many people run it, and on which versions and systems, with [Umami](https://umami.is) at
`umami.sorrycc.dev`, which sorrycc runs. It sends two events:

| Event    | When                                            | What it holds                                         |
| -------- | ----------------------------------------------- | ----------------------------------------------------- |
| `active` | Once a day while Zut is open, with a view of `/` and the version | Zut's version, the system, its architecture |
| `update` | The first launch after Zut updates            | The version before and after, the system, its architecture |

Each also gives the language of Zut's menus. None holds a path, a file's name or contents, a project, a name or your
Mon account. Umami sets no identifier: it tells people apart by a hash of the address and the system, which it changes
each month, and keeps the country and city the address is in, not the address.

To turn it off, set `"usageStats": false` in `settings.json` or in **Settings → General**, or start Zut with
`DO_NOT_TRACK=1`. Builds you make yourself never send it. `usage.json` beside the settings holds the day and version of
the last count, which is all Zut keeps of it.

## Crash reports

After a crash, Zut asks at the next launch whether to send the crash log to Mon at `mon.sorrycc.dev`. It sends one
only when you say so, or when `crashReports` is `"always"`. See [`crashReports`](SETTINGS.md#the-rest).

## Updates

Zut asks GitHub for the latest release once an hour, as any download from GitHub does.

## usemayoi.com

The website counts visits with the same Umami, without cookies, and not at all when your browser sends Do Not Track. It
also counts clicks on the download and GitHub links, and which platform's install command is copied.
