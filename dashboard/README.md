# Outcome Router Audit Dashboard

Private production dashboard for the Outcome Router control plane. It presents:

- verified gross savings against the incumbent counterfactual
- treatment and control outcomes with confidence intervals
- the configured non-inferiority quality floor
- model allocation and policy state
- a reproducible decision ledger
- live connection to `GET /v1/audit/summary`
- local JSON audit export

The dashboard does not persist router credentials. The API key entered in the
connection dialog is held only in component memory for the live request.

```bash
npm install
npm run dev
npm test
```
