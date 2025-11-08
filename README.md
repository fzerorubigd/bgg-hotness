BGG Hotness Tracker
===

[![Powered by BoardGameGeek](https://cf.geekdo-images.com/HZy35cmzmmyV9BarSuk6ug__small/img/gbE7sulIurZE_Tx8EQJXnZSKI6w=/fit-in/200x150/filters:strip_icc()/pic7779581.png)](https://boardgamegeek.com)

What is this?
---
This is a tracker that get the [BGG hotness](https://boardgamegeek.com/hotness) on each day, and the record it in a google spreadsheet. 
Then every week on Monday, it combine the pass 14 days and creates a aggregated version based on the [Schulze method](https://en.wikipedia.org/wiki/Schulze_method) (it consider each day as a vote) 


Why?
--- 

We have a Podcast (In Farsi, [پادساعتگرد](https://podcasters.spotify.com/pod/show/podsaatgard)) and we discuss the hotness as a part, but then we need the changes in the past two weeks, so I decided to create this. 

How?
--- 
It uses my [GoBGG](https://github.com/fzerorubigd/gobgg) library to fetch the data and then the [gsheet action](https://github.com/jroehl/gsheet.action) to push the data into google sheet. 