package chat

// This file used to host RegisterChatServerEvents which called
// mmokit.RegisterEvent for each chat-emitted server event. That
// produced an import cycle once the mmokit facade started importing
// pkg/services/chat. The registration moved to chat.go
// (registerChatServerEvents, called from RegisterChatService at
// facade time). The chat service no longer needs to do it itself —
// any process running RegisterChatService has the registry populated
// before Build() so codec lookups during Init succeed.
