# Why v8s stays fast

Performance is v8s's first design constraint. A Kubernetes UI usually gets slow because it downloads every object, keeps it in the browser, and re-renders all of it on every change. v8s avoids that in four ways.

## 1. The agent does the heavy work

A Go **agent** talks to the Kubernetes API. The UI never sees the API server. The agent builds sorted, filtered **views** itself, so a sort or filter over 50,000 rows is a few milliseconds of work in the agent, and the UI asks for "rows 400 to 460 of this view".

## 2. Only the fields you see are kept

When the agent reads a list, it keeps only what the UI and the Problems checks need: a handful of fields per object, with repeated strings stored once. It drops `managedFields` and `last-applied-configuration` at ingest, which are half the bytes of a typical object. Built-in types are requested as protobuf, which decodes about six times faster than JSON.

The numbers below come from a spike that decoded 10,000 deliberately heavy pods (median 11.7 KB of JSON each) on an M1 Max:

| Measured, 10k pods                                 | Result           |
| -------------------------------------------------- | ---------------- |
| Parse as JSON in V8                                | 473 ms           |
| Go JSON decode into typed objects                  | 614 ms           |
| Go protobuf decode into typed objects              | 101 ms           |
| Agent memory holding full objects                  | about 180 MB     |
| Agent memory holding projected rows                | 14 MB            |
| What a UI window of 9-column rows needs            | 2.3 MB           |
| Send full objects across a thread boundary vs rows | 487 ms vs 3.7 ms |
| Sort 25k rows by name / filter by substring        | 5.3 ms / 0.6 ms  |

The spike's code and results are in the [decode-bench README](https://github.com/zyno-io/v8s/blob/main/spikes/decode-bench/README.md).

## 3. The UI shows a window, not a list

The UI holds only the rows around what you are looking at, usually a few hundred, however large the cluster is. Rows have a fixed height, and the table only renders what is on screen. Data arrives as small patches, at most once per screen frame, and the agent remembers what it last sent, so reconnecting costs one window, not a full reload. A bigger cluster makes the agent's work bigger, not the page's.

## 4. v8s is gentle on your API servers

v8s must never be the reason a cluster is slow. It uses streaming lists (WatchList) where the cluster supports them, so the API server serves the initial state from cache without a big allocation. It honors rate limits and `429` responses, and it caps its concurrent requests and watches per cluster. Large objects such as vulnerability reports are watched as tiny table rows and fetched in full only when you open one. Deselected clusters release their memory. CPU and memory usage is read from metrics-server only while a visible table shows usage columns (or sorts by one), every 30 seconds, and only for the namespaces on screen.

## The budgets

These are the targets v8s is built against. They are design budgets, not guarantees on your hardware: the design gives each one a benchmark in CI that fails the build on regression.

| Metric                                    | Desktop (local agent)                                      | Hosted, desktop browser                        | Phone                    |
| ----------------------------------------- | ---------------------------------------------------------- | ---------------------------------------------- | ------------------------ |
| Cold start to interactive                 | 1.5 s                                                      | 1.5 s                                          | 3.5 s                    |
| Select a cluster to first rows (10k pods) | 1.0 s p95                                                  | 1.0 s + network                                | as hosted                |
| Full sync of 50k pods on a LAN            | 8 s                                                        | 8 s                                            | n/a                      |
| Sort or filter over 50k rows              | 20 ms in the agent, 50 ms on screen                        | 50 ms + network                                | 150 ms + network         |
| Scrolling                                 | 60 fps                                                     | 60 fps                                         | 8 ms scripting per frame |
| Cluster event to visible row, p95         | 150 ms                                                     | 400 ms                                         | 1.5 s                    |
| Sustained churn                           | 1,000 events/s with the main thread under 10% busy         | same                                           | n/a                      |
| Agent memory                              | 50 MB base plus 50 MB per 10k pods; 600 MB at 200k objects | 50 MB per 10k pods per active user and cluster | n/a                      |
| UI memory                                 | 80 MB; 10k to 200k objects adds at most 10 MB              | same                                           | 50 MB                    |
| Log streaming                             | 5,000 lines/s                                              | same                                           | 1,000 lines/s            |
| Idle data with Problems open              | n/a                                                        | 30 KB/min                                      | 30 KB/min, 0 when hidden |

Reference hardware is an M1 MacBook Pro for desktop, and a Pixel 6a-class phone on throttled 4G.

## Read more

The full design, including the wire protocol and the checks that run in CI, is in [Performance architecture](/performance). The [agent and UI protocol](/protocol) is documented too.
