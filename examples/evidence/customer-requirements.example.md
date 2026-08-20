# Customer order requirements

- Every imported order must retain the customer's external reference.
- Reprocessing the same external reference must not create a duplicate order.
- A rejected order must expose a stable reason code to the customer-facing API.
