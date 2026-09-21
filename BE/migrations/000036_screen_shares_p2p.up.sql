-- Phase 36: screen sharing is peer-to-peer WebRTC signaled through ku's
-- own workspace WebSocket. The LiveKit room concept (and the SFU) is gone;
-- drop its column. Shares are identified by id and presenter only.
ALTER TABLE screen_shares DROP COLUMN room_name;
