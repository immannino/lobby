# Chunky.AI

## TODO

1. Finish API endpoint stuff for CRUD session things
2. Get Website redirect stuff built out
3. HTMX create/join endpoints
4. styling
5. Game loop:
        1. Create
        2. Join
        3. Lobby
        4. Ready + Start
        5. Game:
                1. Question asked
                2. Request Inputs
                3. Wait for all inputs
                        1. If no input in timer, then auto-generate input
                4. Generate AI response
                5. Prompt users to pick AI response
                6. Wait for all inputs
                        1. If no input in timer, no points for user
                7. Show winners
                8. Loop 2x more times
        6. At the end of 3 rows, display Scoreboard.
        7. End + ASk to retry => Send to lobby (3)
