# Selected Tremor Raw components

The read-only charts under `src/components/charts/` are adapted from Tremor Raw
v1.0.0, https://github.com/tremorlabs/tremor, retrieved on 2026-11-08:

| Component  | Upstream path                            | Git blob                                 |
| ---------- | ---------------------------------------- | ---------------------------------------- |
| BarList    | src/components/BarList/BarList.tsx       | 72fc0e9c901f89bb7e226e5cfd38e622fdbe1f90 |
| DonutChart | src/components/DonutChart/DonutChart.tsx | 4ef0d347f73acb6290cb83a775ebb78c0af62827 |
| License    | LICENSE                                  | 72bd6f8e43508d4c324a29063b9b0ef3f70133cd |

The complete upstream Apache-2.0 license and bundled subcomponent notices are
retained in `public/licenses/tremor-raw-LICENSE.txt` and ship with the application.
The bundled license matches the upstream Git blob listed above.
No old Tremor package is installed.

Changes: selected read-only APIs, typed name/value data, shared application
semantic colors, responsive layouts, textual equivalents, no data animation,
no links or sector selection, and removal of the Recharts 2-only `activeIndex`
API for Recharts 3 compatibility. BarList preserves the upstream stable sort,
zero-width handling and minimum nonzero bar width. DonutChart preserves the
upstream responsive Pie composition, label calculation, category colors,
clockwise geometry and tooltip composition.

Mantine, Ant Design X, Ant Design, React Flow, Recharts and other npm dependencies
retain their respective license files in the installed packages. Local font
licenses remain under `public/fonts/`.
