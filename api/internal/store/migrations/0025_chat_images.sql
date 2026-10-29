-- Current image bytes stay in memory; saved chat history holds safe metadata only.
alter table chat_messages add column attachments jsonb not null default '[]';
