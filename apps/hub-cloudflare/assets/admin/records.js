export function renderRecords(container, records, title) {
  const nodes = records.map((record) => {
    const card = container.ownerDocument.createElement("article");
    const heading = container.ownerDocument.createElement("h3");
    const details = container.ownerDocument.createElement("pre");
    heading.textContent = title(record);
    details.textContent = JSON.stringify(record, null, 2);
    card.append(heading);
    card.append(details);
    return card;
  });
  container.replaceChildren(...nodes);
}
